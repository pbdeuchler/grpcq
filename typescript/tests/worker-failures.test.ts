import { expect, it } from "vitest";
import { createDefaultMessage, Registry, Server, Worker, WorkerPool, type Receipt, type QueueAdapter } from "../src/index.js";
import { MemoryAdapter } from "../src/adapters/memory.js";

it.each([false, true].flatMap(pooled =>
  ["success", "handler", "ack", "handler and nack"].map(mode => ({ pooled, mode })),
))(
  "reports $mode outcome even during cancellation (pooled=$pooled)", async ({ pooled, mode }) => {
    const controller = new AbortController();
    const handlerError = new Error("handler failed");
    const ackError = new Error("delete failed");
    const nackError = new Error("requeue failed");
    const operations: string[] = [];
    const receipt: Receipt = {
      ack: async () => { operations.push("ack"); controller.abort(); if (mode === "ack") throw ackError; },
      nack: async () => { operations.push("nack"); controller.abort(); if (mode === "handler and nack") throw nackError; },
    };
    let delivered = false;
    const adapter: QueueAdapter = {
      publish: async () => {},
      consume: async () => {
        if (delivered) return { items: [] };
        delivered = true;
        return { items: [{ message: { ...createDefaultMessage(), messageId: "message-1", topic: "svc", action: "call" }, receipt }] };
      },
    };
    const registry = new Registry();
    registry.register("svc", "call", async () => { if (mode.includes("handler")) throw handlerError; });
    const config = { queueName: "q", concurrency: 1 };
    const worker = pooled ? new WorkerPool(adapter, registry, config, 2) : new Worker(adapter, registry, config);
    const result: unknown = await worker.start(controller.signal).catch((error: unknown) => error);
    expect(operations).toEqual([mode.includes("handler") ? "nack" : "ack"]);
    if (mode === "success") {
      expect(result).toMatchObject({ code: "CANCELLED" });
    } else {
      expect(result).toMatchObject({ code: "MESSAGE_PROCESSING" });
      expect((result as Error).message).toContain("message-1");
      expect((result as Error).message).toContain("svc/call");
      if (mode === "handler and nack") {
        expect((result as Error & { cause: unknown }).cause).toBeInstanceOf(AggregateError);
        expect((result as Error & { cause: AggregateError }).cause.errors).toEqual([handlerError, nackError]);
      } else expect(result).toMatchObject({ cause: mode === "ack" ? ackError : handlerError });
    }
  },
);

it.each([false, true])("poison messages stop intake and preserve the received batch (registered=%s)", async registered => {
  const memory = new MemoryAdapter(2);
  await memory.publish("q", ["bad", "good"].map(messageId => ({ ...createDefaultMessage(), messageId, topic: "svc", action: "call" })));
  const registry = new Registry();
  let calls = 0;
  if (registered) registry.register("svc", "call", async msg => { calls++; if (msg.messageId === "bad") throw new Error("permanent failure"); });
  const worker = new Worker(memory, registry, { queueName: "q", concurrency: 1, maxBatch: 2 });
  await expect(worker.start()).rejects.toMatchObject({ code: "MESSAGE_PROCESSING" });
  expect(calls).toBe(registered ? 2 : 0);
  expect(memory.queueDepth("q")).toBe(registered ? 1 : 2);
});

it("a processing failure stops the pool's other workers", async () => {
  const memory = new MemoryAdapter(1);
  await memory.publish("q", [{ ...createDefaultMessage(), topic: "missing", action: "call", messageId: "poison" }]);
  const pool = new WorkerPool(memory, new Registry(), { queueName: "q", pollIntervalMs: 60_000 }, 2);
  await expect(pool.start()).rejects.toMatchObject({ code: "MESSAGE_PROCESSING" });
  expect(memory.queueDepth("q")).toBe(1);
});

it("typed request decode failures surface without calling the handler", async () => {
  const memory = new MemoryAdapter(1);
  await memory.publish("q", [{ ...createDefaultMessage(), topic: "svc", action: "call", messageId: "invalid-request", payload: new Uint8Array([255]) }]);
  const server = new Server(memory, { queueName: "q" });
  const cause = new Error("invalid request");
  let calls = 0;
  server.registerMethod("svc", "call", () => { throw cause; }, async () => { calls++; });
  await expect(server.start()).rejects.toMatchObject({ code: "MESSAGE_PROCESSING", cause: { code: "REQUEST_DECODE", cause } });
  expect(calls).toBe(0);
  expect(memory.queueDepth("q")).toBe(1);
});
