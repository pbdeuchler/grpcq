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
    await item.receipt.ack().catch(() => {});
  } catch {
    await item.receipt.nack().catch(() => {});
  }
}

function delay(ms: number, signal?: AbortSignal): Promise<"timeout" | "abort"> {
  if (signal?.aborted) return Promise.resolve("abort");
  if (ms <= 0) return Promise.resolve("timeout");

  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve("timeout");
    }, ms);

    function onAbort(): void {
      clearTimeout(timer);
      resolve("abort");
    }

    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

class InFlightPool {
  private active = new Set<Promise<void>>();
  private resolveWaiter: (() => void) | null = null;

  get size(): number {
    return this.active.size;
  }

  add(promise: Promise<void>): void {
    const tracked = promise.finally(() => {
      this.active.delete(tracked);
      if (this.resolveWaiter) {
        const resolve = this.resolveWaiter;
        this.resolveWaiter = null;
        resolve();
      }
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
      await this.run(signal);
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

  private isStopping(externalSignal?: AbortSignal): boolean {
    return this.stopController.signal.aborted || (externalSignal?.aborted ?? false);
  }

  private async run(externalSignal?: AbortSignal): Promise<void> {
    const inFlight = new InFlightPool();
    let cancelError: GrpcqError | null = null;

    const loop = async (): Promise<void> => {
      while (!this.isStopping(externalSignal)) {
        // Wait for concurrency slot
        while (inFlight.size >= this.concurrency) {
          if (this.isStopping(externalSignal)) break;
          await inFlight.waitForOne();
        }

        if (this.stopController.signal.aborted) break;
        if (externalSignal?.aborted) {
          cancelError = GrpcqError.cancelled();
          break;
        }

        // Consume batch
        let result;
        try {
          result = await this.adapter.consume(this.queueName, this.maxBatch);
        } catch (err) {
          cancelError =
            err instanceof GrpcqError
              ? err
              : GrpcqError.other(String(err), err);
          break;
        }

        if (result.items.length === 0) {
          // Wait for poll interval, but break early if a stop/cancel arrives
          // or an inflight task completes
          if (inFlight.size > 0) {
            const raceResult = await Promise.race([
              inFlight.waitForOne().then(() => "inflight" as const),
              delay(this.pollIntervalMs, this.stopController.signal),
            ]);
            if (raceResult === "abort" && !this.stopController.signal.aborted) {
              // External signal won the race
            }
          } else {
            const result = await delay(
              this.pollIntervalMs,
              this.stopController.signal,
            );
            if (result === "abort" && externalSignal?.aborted) {
              cancelError = GrpcqError.cancelled();
              break;
            }
          }
          continue;
        }

        // Dispatch messages
        for (const item of result.items) {
          while (inFlight.size >= this.concurrency) {
            if (this.isStopping(externalSignal)) break;
            await inFlight.waitForOne();
          }

          if (this.isStopping(externalSignal)) break;

          inFlight.add(processMessage(this.registry, item));
        }
      }
    };

    await loop();

    // Drain all in-flight work before returning
    await inFlight.drain();

    if (cancelError) throw cancelError;
  }
}

export class WorkerPool {
  private readonly workers: Worker[];
  private state = WorkerState.Idle;
  private stopController = new AbortController();
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

    // Combine external signal with internal stop
    const combinedController = new AbortController();
    const onExternalAbort = (): void => combinedController.abort();
    const onStopAbort = (): void => combinedController.abort();
    signal?.addEventListener("abort", onExternalAbort, { once: true });
    this.stopController.signal.addEventListener("abort", onStopAbort, {
      once: true,
    });

    try {
      const results = await Promise.allSettled(
        this.workers.map((w) => w.start(combinedController.signal)),
      );

      // If any worker failed, stop all and propagate first error
      const firstError = results.find(
        (r): r is PromiseRejectedResult => r.status === "rejected",
      );

      if (firstError) {
        await this.stopAll();
        throw firstError.reason;
      }
    } finally {
      signal?.removeEventListener("abort", onExternalAbort);
      this.stopController.signal.removeEventListener("abort", onStopAbort);
      this.state = WorkerState.Stopped;
      this.finishedResolve?.();
    }
  }

  async stop(): Promise<void> {
    this.stopController.abort();
    await this.stopAll();
    if (this.state === WorkerState.Idle) return;
    await this.finishedPromise;
  }

  private async stopAll(): Promise<void> {
    await Promise.all(this.workers.map((w) => w.stop()));
  }
}
