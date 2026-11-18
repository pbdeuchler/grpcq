// pattern: Imperative Shell

import { GrpcqError } from "./error.js";
import { Registry } from "./registry.js";
import type { MessageItem, QueueAdapter } from "./types.js";

export interface WorkerConfig {
  queueName: string;
  maxBatch?: number;
  concurrency?: number;
  pollIntervalMs?: number;
}

const enum WorkerState {
  Idle = 0,
  Running = 1,
  Stopped = 2,
}

async function processMessage(
  registry: Registry,
  item: MessageItem,
): Promise<void> {
  try {
    await registry.handle(item.message);
  } catch (handlerError) {
    let cause = handlerError;
    try {
      await item.receipt.nack();
    } catch (nackError) {
      cause = new AggregateError(
        [handlerError, nackError],
        `${handlerError}; failed to nack message: ${nackError}`,
      );
    }
    throw GrpcqError.messageProcessing(item.message, "handle", cause);
  }
  try {
    await item.receipt.ack();
  } catch (cause) {
    throw GrpcqError.messageProcessing(item.message, "ack", cause);
  }
}

function delay(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve();

  return new Promise((resolve) => {
    const timer = setTimeout(finish, ms);

    function finish(): void {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    }

    signal.addEventListener("abort", finish, { once: true });
  });
}

class InFlightPool {
  private active = new Set<Promise<void>>();
  private resolveWaiter: (() => void) | null = null;

  constructor(private readonly onFailure: (error: unknown) => void) {}

  get size(): number {
    return this.active.size;
  }

  add(promise: Promise<void>): void {
    const tracked = promise.catch(this.onFailure).finally(() => {
      this.active.delete(tracked);
      this.resolveWaiter?.();
      this.resolveWaiter = null;
    });
    this.active.add(tracked);
  }

  waitForOne(): Promise<void> {
    if (this.active.size === 0) return Promise.resolve();
    return new Promise((resolve) => {
      this.resolveWaiter = resolve;
    });
  }

  async drain(): Promise<void> {
    while (this.active.size > 0) {
      await this.waitForOne();
    }
  }
}

export class Worker {
  private readonly maxBatch: number;
  private readonly concurrency: number;
  private readonly pollIntervalMs: number;
  private readonly queueName: string;
  private state = WorkerState.Idle;
  private stopController = new AbortController();
  private finishedResolve: (() => void) | null = null;
  private finishedPromise: Promise<void>;

  constructor(
    private readonly adapter: QueueAdapter,
    private readonly registry: Registry,
    config: WorkerConfig,
  ) {
    this.queueName = config.queueName;
    this.maxBatch = Math.max(config.maxBatch ?? 10, 1);
    this.concurrency = Math.max(config.concurrency ?? 10, 1);
    this.pollIntervalMs = config.pollIntervalMs ?? 1000;
    this.finishedPromise = new Promise((resolve) => {
      this.finishedResolve = resolve;
    });
  }

  async start(signal?: AbortSignal): Promise<void> {
    if (this.state !== WorkerState.Idle) {
      throw GrpcqError.workerAlreadyStarted();
    }
    this.state = WorkerState.Running;

    try {
      const intake = signal
        ? AbortSignal.any([signal, this.stopController.signal])
        : this.stopController.signal;
      await this.run(intake);
      if (signal?.aborted) throw GrpcqError.cancelled();
    } finally {
      this.state = WorkerState.Stopped;
      this.finishedResolve?.();
    }
  }

  async stop(): Promise<void> {
    this.stopController.abort();
    if (this.state === WorkerState.Idle) return;
    await this.finishedPromise;
  }

  private async run(signal: AbortSignal): Promise<void> {
    let failure: GrpcqError | null = null;
    const inFlight = new InFlightPool((error) => {
      failure ??= error instanceof GrpcqError
        ? error
        : GrpcqError.other(String(error), error);
      this.stopController.abort();
    });
    let outcome: GrpcqError | null = null;
    try {
      while (!signal.aborted) {
        while (inFlight.size >= this.concurrency) await inFlight.waitForOne();
        if (signal.aborted) break;

        const result = await this.adapter.consume(
          this.queueName,
          this.maxBatch,
          signal,
        );
        if (result.items.length === 0) {
          await delay(this.pollIntervalMs, signal);
          continue;
        }

        // Ownership transfers on receive; finish this batch even during shutdown.
        for (const item of result.items) {
          while (inFlight.size >= this.concurrency) await inFlight.waitForOne();
          inFlight.add(processMessage(this.registry, item));
        }
      }
    } catch (error) {
      if (!signal.aborted) {
        outcome = error instanceof GrpcqError
          ? error
          : GrpcqError.other(String(error), error);
      }
    } finally {
      await inFlight.drain();
    }
    if (outcome) throw outcome;
    if (failure) throw failure;
  }
}

export class WorkerPool {
  private readonly workers: Worker[];
  private state = WorkerState.Idle;
  private finishedResolve: (() => void) | null = null;
  private finishedPromise: Promise<void>;

  constructor(
    adapter: QueueAdapter,
    registry: Registry,
    config: WorkerConfig,
    numWorkers: number,
  ) {
    const count = Math.max(numWorkers, 1);
    this.workers = Array.from(
      { length: count },
      () => new Worker(adapter, registry, config),
    );
    this.finishedPromise = new Promise((resolve) => {
      this.finishedResolve = resolve;
    });
  }

  async start(signal?: AbortSignal): Promise<void> {
    if (this.state !== WorkerState.Idle) {
      throw GrpcqError.workerPoolAlreadyStarted();
    }
    this.state = WorkerState.Running;

    let failure: unknown;
    const tasks = this.workers.map(async (worker) => {
      try {
        await worker.start(signal);
      } catch (error) {
        if (!(error instanceof GrpcqError && error.code === "CANCELLED")) {
          failure ??= error;
        }
        throw error;
      }
    });
    try {
      await Promise.all(tasks);
    } catch (error) {
      // Stop siblings as soon as one fails, then drain before returning the error.
      await this.stopAll();
      await Promise.allSettled(tasks);
      throw failure ?? error;
    } finally {
      this.state = WorkerState.Stopped;
      this.finishedResolve?.();
    }
  }

  async stop(): Promise<void> {
    await this.stopAll();
    if (this.state === WorkerState.Idle) return;
    await this.finishedPromise;
  }

  private async stopAll(): Promise<void> {
    await Promise.all(this.workers.map((w) => w.stop()));
  }
}
