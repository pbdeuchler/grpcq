import { describe, expect, it } from "vitest";
import {
  createDefaultMessage, GrpcqError, Registry, Worker, WorkerPool,
  type ConsumeResult, type QueueAdapter,
} from "../src/index.js";
import { MemoryAdapter } from "../src/adapters/memory.js";

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
const message = () => ({ ...createDefaultMessage(), topic: "svc", action: "call" });

function blockedReceive(signal?: AbortSignal): Promise<ConsumeResult> {
  return new Promise((_, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
}

describe("worker lifecycle", () => {
  it.each(["stop", "cancel"] as const)("%s drains the entire received batch", async (operation) => {
    const memory = new MemoryAdapter(2);
    await memory.publish("q", [message(), message()]);
    const received = deferred();
    const releaseReceive = deferred();
    const adapter: QueueAdapter = {
      publish: (queue, messages) => memory.publish(queue, messages),
      consume: async () => {
        const batch = await memory.consume("q", 2);
        received.resolve();
        await releaseReceive.promise;
        return batch;
      },
    };
    const registry = new Registry();
    const started = deferred();
    const releaseHandler = deferred();
    let calls = 0;
    registry.register("svc", "call", async () => {
      calls++;
      if (calls === 1) { started.resolve(); await releaseHandler.promise; }
    });
    const controller = new AbortController();
    const worker = new Worker(adapter, registry, { queueName: "q", concurrency: 1 });
    let finished = false;
    const running = worker.start(controller.signal).then(
      () => { finished = true; return null; },
      (error: unknown) => { finished = true; return error; },
    );
    await received.promise;
    const stopping = operation === "stop" ? worker.stop() : undefined;
    if (operation === "cancel") controller.abort();
    releaseReceive.resolve();
    await started.promise;
    expect(finished).toBe(false);
    releaseHandler.resolve();
    const result = await running;
    if (operation === "cancel") expect(result).toMatchObject({ code: "CANCELLED" });
    else expect(result).toBeNull();
    await stopping;
    expect(calls).toBe(2);
    await memory.publish("q", [message(), message()]);
  });

  it.each(["stop", "cancel"] as const)("%s interrupts a pending receive", async operation => {
    const receiving = deferred();
    const adapter: QueueAdapter = {
      publish: async () => {},
      consume: (_queue, _batch, signal) => { receiving.resolve(); return blockedReceive(signal); },
    };
    const controller = new AbortController();
    const worker = new Worker(adapter, new Registry(), { queueName: "q" });
    const running = worker.start(controller.signal).catch((error: unknown) => error);
    await receiving.promise;
    if (operation === "stop") { await worker.stop(); expect(await running).toBeUndefined(); }
    else { controller.abort(); expect(await running).toMatchObject({ code: "CANCELLED" }); }
  });

  it("external cancellation interrupts the polling delay", async () => {
    const polled = deferred();
    const controller = new AbortController();
    const worker = new Worker({
      publish: async () => {},
      consume: async () => { polled.resolve(); return { items: [] }; },
    }, new Registry(), { queueName: "q", pollIntervalMs: 60_000 });
    const running = worker.start(controller.signal).catch((error: unknown) => error);
    await polled.promise;
    controller.abort();
    expect(await running).toMatchObject({ code: "CANCELLED" });
  });

  it("returns failed received messages during shutdown", async () => {
    const memory = new MemoryAdapter(1);
    await memory.publish("q", [message()]);
    const controller = new AbortController();
    const registry = new Registry();
    registry.register("svc", "call", async () => { controller.abort(); throw new Error("handler failed"); });
    const worker = new Worker(memory, registry, { queueName: "q", concurrency: 1 });
    await expect(worker.start(controller.signal)).rejects.toMatchObject({ code: "MESSAGE_PROCESSING" });
    expect(memory.queueDepth("q")).toBe(1);
  });

  it("drains received work before reporting a receive failure", async () => {
    const memory = new MemoryAdapter(1);
    await memory.publish("q", [message()]);
    const failure = new Error("receive failed");
    const failed = deferred();
    let polls = 0;
    const adapter: QueueAdapter = {
      publish: async () => {},
      consume: async () => {
        if (polls++ === 0) return memory.consume("q", 1);
        failed.resolve();
        throw failure;
      },
    };
    const release = deferred();
    const registry = new Registry();
    registry.register("svc", "call", async () => { await release.promise; });
    const worker = new Worker(adapter, registry, { queueName: "q" });
    let finished = false;
    const running = worker.start().catch((error: unknown) => { finished = true; return error; });
    await failed.promise;
    await expect(memory.publish("q", [message()])).rejects.toMatchObject({ code: "QUEUE_FULL" });
    expect(finished).toBe(false);
    release.resolve();
    expect(await running).toMatchObject({ cause: failure });
    await memory.publish("q", [message()]);
  });

  it.each(["pre-cancelled", "pre-stopped"] as const)("handles %s start", async mode => {
    let polls = 0;
    const controller = new AbortController();
    const worker = new Worker({ publish: async () => {}, consume: async () => { polls++; return { items: [] }; } }, new Registry(), { queueName: "q" });
    if (mode === "pre-cancelled") controller.abort();
    else await worker.stop();
    if (mode === "pre-cancelled") await expect(worker.start(controller.signal)).rejects.toMatchObject({ code: "CANCELLED" });
    else await worker.start();
    expect(polls).toBe(0);
    await expect(worker.start()).rejects.toMatchObject({ code: "WORKER_ALREADY_STARTED" });
  });
  it("rejects a concurrent start", async () => {
    const receiving = deferred();
    const worker = new Worker({
      publish: async () => {},
      consume: (_queue, _batch, signal) => { receiving.resolve(); return blockedReceive(signal); },
    }, new Registry(), { queueName: "q" });
    const running = worker.start();
    await receiving.promise;
    await expect(worker.start()).rejects.toMatchObject({ code: "WORKER_ALREADY_STARTED" });
    await worker.stop();
    await running;
  });

  it("preserves structured receive errors", async () => {
    const failure = GrpcqError.other("receive failed");
    const worker = new Worker({
      publish: async () => {},
      consume: async () => { throw failure; },
    }, new Registry(), { queueName: "q" });
    await expect(worker.start()).rejects.toBe(failure);
  });
});

describe("worker pool lifecycle", () => {
  it("stops healthy siblings on the first receive failure", async () => {
    const siblingStarted = deferred();
    const failure = new Error("receive failed");
    let polls = 0;
    const adapter: QueueAdapter = {
      publish: async () => {},
      consume: async (_queue, _batch, signal) => {
        if (polls++ === 0) { siblingStarted.resolve(); return blockedReceive(signal); }
        await siblingStarted.promise;
        throw failure;
      },
    };
    const pool = new WorkerPool(adapter, new Registry(), { queueName: "q" }, 2);
    await expect(pool.start()).rejects.toMatchObject({ cause: failure });
    await pool.stop();
  });

  it.each(["stop", "cancel", "pre-stopped", "pre-cancelled"] as const)("handles %s", async operation => {
    const receiving = deferred();
    let polls = 0;
    const adapter: QueueAdapter = {
      publish: async () => {},
      consume: (_queue, _batch, signal) => { polls++; receiving.resolve(); return blockedReceive(signal); },
    };
    const controller = new AbortController();
    const pool = new WorkerPool(adapter, new Registry(), { queueName: "q" }, 2);
    if (operation === "pre-stopped") await pool.stop();
    if (operation === "pre-cancelled") controller.abort();
    const running = pool.start(controller.signal).catch((error: unknown) => error);
    if (operation === "stop" || operation === "cancel") {
      await receiving.promise;
      if (operation === "stop") await pool.stop();
      else controller.abort();
    }
    const result = await running;
    if (operation.includes("cancel")) expect(result).toMatchObject({ code: "CANCELLED" });
    else expect(result).toBeUndefined();
    if (operation.startsWith("pre-")) expect(polls).toBe(0);
    await expect(pool.start()).rejects.toMatchObject({ code: "WORKER_POOL_ALREADY_STARTED" });
  });
});

it("keeps processing within the concurrency limit while a handler is suspended", async () => {
  const memory = new MemoryAdapter(8);
  await memory.publish("q", Array.from({ length: 8 }, message));
  const release = deferred();
  const controller = new AbortController();
  let calls = 0;
  let active = 0;
  let maximum = 0;
  const registry = new Registry();
  registry.register("svc", "call", async () => {
    const call = ++calls;
    maximum = Math.max(maximum, ++active);
    if (call === 1) await release.promise;
    if (call === 8) { controller.abort(); release.resolve(); }
    active--;
  });
  const worker = new Worker(memory, registry, { queueName: "q", concurrency: 2 });
  await expect(worker.start(controller.signal)).rejects.toMatchObject({ code: "CANCELLED" });
  expect(calls).toBe(8);
  expect(maximum).toBe(2);
  await memory.publish("q", Array.from({ length: 8 }, message));
});
