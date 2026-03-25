import { describe, expect, it } from "vitest";
import {
  type CallOptions,
  Client,
  type ConsumeResult,
  GrpcqError,
  MAX_MESSAGE_SIZE,
  type Message,
  type MessageItem,
  Producer,
  type QueueAdapter,
  type Receipt,
  Registry,
  Server,
  Worker,
  type WorkerConfig,
  createDefaultMessage,
  decodeMessage,
  encodeMessage,
} from "../src/index.js";
import { MemoryAdapter } from "../src/adapters/memory.js";
import {
  decodeMessageBody,
  encodeMessageBody,
} from "../src/adapters/sqs.js";

// -- Helpers --

/** Simple proto-like encode: length-prefixed string (field 1, wire type 2) */
function encodeTestRequest(name: string): Uint8Array {
  const encoded = new TextEncoder().encode(name);
  // protobuf: tag (field 1, wire type 2 = 0x0a), length, bytes
  return new Uint8Array([0x0a, encoded.length, ...encoded]);
}

function decodeTestRequest(data: Uint8Array): { name: string } {
  // Skip tag byte and length byte
  if (data.length < 2 || data[0] !== 0x0a) {
    throw new Error("invalid test request encoding");
  }
  const len = data[1]!;
  return { name: new TextDecoder().decode(data.slice(2, 2 + len)) };
}

class RecordingAdapter implements QueueAdapter {
  queueName: string | null = null;
  messages: Message[] = [];

  async publish(queueName: string, messages: Message[]): Promise<void> {
    this.queueName = queueName;
    this.messages.push(...messages.map((m) => structuredClone(m)));
  }

  async consume(): Promise<ConsumeResult> {
    return { items: [] };
  }
}

class StubAdapter implements QueueAdapter {
  private item: MessageItem | null;

  constructor(item: MessageItem) {
    this.item = item;
  }

  async publish(): Promise<void> {}

  async consume(): Promise<ConsumeResult> {
    const item = this.item;
    this.item = null;
    return { items: item ? [item] : [] };
  }
}

function createTestReceipt(onAck?: () => void): Receipt & {
  acked: boolean;
  nacked: boolean;
} {
  const receipt = {
    acked: false,
    nacked: false,
    ack: async () => {
      receipt.acked = true;
      onAck?.();
    },
    nack: async () => {
      receipt.nacked = true;
    },
  };
  return receipt;
}

// -- Tests --

describe("Producer", () => {
  it("send populates the message envelope", async () => {
    const adapter = new RecordingAdapter();
    const producer = new Producer(adapter, "test-producer");

    const metadata = { "trace-id": "123" };
    await producer.send(
      "test-queue",
      "svc.Service",
      "DoThing",
      encodeTestRequest("alice"),
      metadata,
    );

    expect(adapter.messages).toHaveLength(1);
    const msg = adapter.messages[0]!;
    expect(msg.originator).toBe("test-producer");
    expect(msg.topic).toBe("svc.Service");
    expect(msg.action).toBe("DoThing");
    expect(msg.messageId).toBeTruthy();
    expect(msg.timestampMs).toBeGreaterThan(0);
    expect(msg.metadata["trace-id"]).toBe("123");

    const decoded = decodeTestRequest(msg.payload);
    expect(decoded.name).toBe("alice");
  });

  it("validates inputs", async () => {
    const adapter = new RecordingAdapter();
    const producer = new Producer(adapter, "origin");

    // Empty queue name
    await expect(
      producer.send("", "svc.Service", "DoThing", new Uint8Array(0)),
    ).rejects.toThrow(GrpcqError);

    await expect(
      producer.send("", "svc.Service", "DoThing", new Uint8Array(0)),
    ).rejects.toMatchObject({ code: "EMPTY_QUEUE_NAME" });

    // Empty topic
    await expect(
      producer.send("queue", "", "DoThing", new Uint8Array(0)),
    ).rejects.toMatchObject({ code: "EMPTY_TOPIC" });

    // Oversized message
    const oversized = new Uint8Array(MAX_MESSAGE_SIZE + 1);
    await expect(
      producer.send("queue", "svc.Service", "DoThing", oversized),
    ).rejects.toMatchObject({ code: "MESSAGE_TOO_LARGE" });
  });
});

describe("Registry", () => {
  it("routes messages and reports missing handlers", async () => {
    const registry = new Registry();
    let called = false;

    registry.register("svc.Service", "DoThing", async () => {
      called = true;
    });

    await registry.handle({
      ...createDefaultMessage(),
      topic: "svc.Service",
      action: "DoThing",
    });

    expect(called).toBe(true);

    // Unknown topic
    await expect(
      registry.handle({
        ...createDefaultMessage(),
        topic: "unknown.Service",
        action: "DoThing",
      }),
    ).rejects.toMatchObject({ code: "UNKNOWN_TOPIC" });

    // Unknown action
    await expect(
      registry.handle({
        ...createDefaultMessage(),
        topic: "svc.Service",
        action: "Unknown",
      }),
    ).rejects.toMatchObject({ code: "UNKNOWN_ACTION" });
  });

  it("reports registration status and introspection", () => {
    const registry = new Registry();
    registry.register("svc.A", "Do", async () => {});
    registry.register("svc.B", "Go", async () => {});

    expect(registry.isRegistered("svc.A", "Do")).toBe(true);
    expect(registry.isRegistered("svc.A", "Missing")).toBe(false);
    expect(registry.isRegistered("missing", "Do")).toBe(false);

    expect(registry.topics().sort()).toEqual(["svc.A", "svc.B"]);
    expect(registry.actions("svc.A")).toEqual(["Do"]);
    expect(registry.actions("missing")).toBeUndefined();
  });
});

describe("MemoryAdapter", () => {
  it("supports publish, consume, ack, nack, and all-or-nothing semantics", async () => {
    const adapter = new MemoryAdapter(2);

    // Initial publish succeeds
    await adapter.publish("queue", [
      { ...createDefaultMessage(), messageId: "msg-1" },
    ]);

    // Batch publish fails when queue lacks capacity (all-or-nothing)
    await expect(
      adapter.publish("queue", [
        { ...createDefaultMessage(), messageId: "msg-2" },
        { ...createDefaultMessage(), messageId: "msg-3" },
      ]),
    ).rejects.toMatchObject({ code: "QUEUE_FULL" });
    expect(adapter.queueDepth("queue")).toBe(1);

    // Consume the message
    const result = await adapter.consume("queue", 10);
    expect(result.items).toHaveLength(1);

    // Nack requeues
    await result.items[0]!.receipt.nack();
    expect(adapter.queueDepth("queue")).toBe(1);

    // Consume again and ack
    const result2 = await adapter.consume("queue", 10);
    await result2.items[0]!.receipt.ack();
    expect(adapter.queueDepth("queue")).toBe(0);
  });

  it("prevents double ack/nack", async () => {
    const adapter = new MemoryAdapter(10);
    await adapter.publish("queue", [
      { ...createDefaultMessage(), messageId: "msg-1" },
    ]);

    const result = await adapter.consume("queue", 10);
    const receipt = result.items[0]!.receipt;

    await receipt.ack();
    await expect(receipt.ack()).rejects.toMatchObject({
      code: "ALREADY_ACKNOWLEDGED",
    });
    await expect(receipt.nack()).rejects.toMatchObject({
      code: "ALREADY_ACKNOWLEDGED",
    });
  });
});

describe("Worker", () => {
  it("waits for in-flight work before returning on cancellation", async () => {
    let handlerStarted = false;
    let handlerFinished = false;
    let releaseHandler: () => void;
    const handlerBlocked = new Promise<void>((resolve) => {
      releaseHandler = resolve;
    });

    const receipt = createTestReceipt();
    const adapter = new StubAdapter({
      message: {
        ...createDefaultMessage(),
        topic: "svc",
        action: "action",
        messageId: "1",
      },
      receipt,
    });

    const registry = new Registry();
    registry.register("svc", "action", async () => {
      handlerStarted = true;
      await handlerBlocked;
      handlerFinished = true;
    });

    const config: WorkerConfig = {
      queueName: "queue",
      concurrency: 1,
      maxBatch: 1,
      pollIntervalMs: 10,
    };

    const abortController = new AbortController();
    const worker = new Worker(adapter, registry, config);
    const workerPromise = worker.start(abortController.signal);

    // Wait for handler to start
    await new Promise<void>((resolve) => {
      const interval = setInterval(() => {
        if (handlerStarted) {
          clearInterval(interval);
          resolve();
        }
      }, 5);
    });

    // Cancel while handler is still running
    abortController.abort();

    // Handler should still be running (not acked yet)
    expect(receipt.acked).toBe(false);
    expect(handlerFinished).toBe(false);

    // Release the handler
    releaseHandler!();

    // Worker should finish and throw cancelled
    await expect(workerPromise).rejects.toMatchObject({ code: "CANCELLED" });

    // Handler should have completed and receipt acked
    expect(handlerFinished).toBe(true);
    expect(receipt.acked).toBe(true);
  });
});

describe("Client", () => {
  it("invoke uses queue override and metadata", async () => {
    const adapter = new RecordingAdapter();
    const client = new Client(adapter, {
      queueName: "default-queue",
      originator: "producer",
    });

    const options: CallOptions = {
      queueName: "override-queue",
      metadata: { "trace-id": "123" },
    };

    await client.invoke(
      "svc.Service",
      "DoThing",
      new Uint8Array(0),
      options,
    );

    expect(adapter.queueName).toBe("override-queue");
    expect(adapter.messages).toHaveLength(1);
    expect(adapter.messages[0]!.originator).toBe("producer");
    expect(adapter.messages[0]!.metadata["trace-id"]).toBe("123");
  });
});

describe("Server", () => {
  it("processes typed requests", async () => {
    const adapter = new MemoryAdapter(16);
    const server = new Server(adapter, {
      queueName: "queue",
      pollIntervalMs: 10,
    });

    let processed: { topic: string; action: string; name: string } | null =
      null;
    const processedPromise = new Promise<void>((resolve) => {
      server.registerMethod<{ name: string }, void>(
        "svc.Service",
        "CreateUser",
        decodeTestRequest,
        async (ctx, req) => {
          processed = {
            topic: ctx.message.topic,
            action: ctx.message.action,
            name: req.name,
          };
          resolve();
        },
      );
    });

    const abortController = new AbortController();
    const serverPromise = server.start(abortController.signal);

    // Publish a message
    const producer = new Producer(adapter, "origin");
    await producer.send(
      "queue",
      "svc.Service",
      "CreateUser",
      encodeTestRequest("alice"),
    );

    // Wait for processing
    await processedPromise;

    expect(processed).toEqual({
      topic: "svc.Service",
      action: "CreateUser",
      name: "alice",
    });

    // Stop server
    await server.stop();
    // Server start should resolve after stop
    await serverPromise.catch(() => {});
  });
});

describe("Message protobuf codec", () => {
  it("round-trips a message", () => {
    const original: Message = {
      originator: "svc",
      topic: "topic",
      action: "action",
      payload: new Uint8Array([0x00, 0xff, 0x10, 0x80]),
      messageId: "id-1",
      timestampMs: 0,
      metadata: {},
    };

    const encoded = encodeMessage(original);
    const decoded = decodeMessage(encoded);

    expect(decoded.originator).toBe(original.originator);
    expect(decoded.topic).toBe(original.topic);
    expect(decoded.action).toBe(original.action);
    expect(decoded.messageId).toBe(original.messageId);
    expect(decoded.payload).toEqual(original.payload);
  });

  it("preserves metadata", () => {
    const original: Message = {
      originator: "svc",
      topic: "topic",
      action: "action",
      payload: new Uint8Array([1, 2, 3]),
      messageId: "id-meta",
      timestampMs: 0,
      metadata: { key: "value", "trace-id": "abc-123" },
    };

    const encoded = encodeMessage(original);
    const decoded = decodeMessage(encoded);

    expect(decoded.metadata).toEqual(original.metadata);
  });

  it("preserves timestamp", () => {
    const original: Message = {
      originator: "svc",
      topic: "topic",
      action: "action",
      payload: new Uint8Array(0),
      messageId: "id-ts",
      timestampMs: 1711234567890,
      metadata: {},
    };

    const encoded = encodeMessage(original);
    const decoded = decodeMessage(encoded);

    expect(decoded.timestampMs).toBe(original.timestampMs);
  });

  it("decodes empty data as default message", () => {
    const decoded = decodeMessage(new Uint8Array(0));
    expect(decoded.messageId).toBe("");
    expect(decoded.timestampMs).toBe(0);
  });
});

describe("SQS encode/decode", () => {
  it("round-trips via base64", () => {
    const original: Message = {
      originator: "svc",
      topic: "topic",
      action: "action",
      payload: new Uint8Array([0x00, 0xff, 0x10, 0x80]),
      messageId: "id-1",
      timestampMs: 1711234567890,
      metadata: { key: "value" },
    };

    const body = encodeMessageBody(original);
    const decoded = decodeMessageBody(body);

    expect(decoded.originator).toBe(original.originator);
    expect(decoded.topic).toBe(original.topic);
    expect(decoded.action).toBe(original.action);
    expect(decoded.messageId).toBe(original.messageId);
    expect(decoded.payload).toEqual(original.payload);
    expect(decoded.metadata).toEqual(original.metadata);
    expect(decoded.timestampMs).toBe(original.timestampMs);
  });

  it("produces valid base64", () => {
    const msg: Message = {
      originator: "svc",
      topic: "t",
      action: "a",
      payload: new Uint8Array([0x00, 0xff]),
      messageId: "id",
      timestampMs: 0,
      metadata: {},
    };

    const body = encodeMessageBody(msg);
    expect(() => Buffer.from(body, "base64")).not.toThrow();
  });

  it("decode invalid body returns error", () => {
    expect(() =>
      decodeMessageBody("not-valid-base64-or-protobuf!!!"),
    ).toThrow();
  });

  it("decode empty body returns default message", () => {
    const msg = decodeMessageBody("");
    expect(msg.messageId).toBe("");
  });
});
