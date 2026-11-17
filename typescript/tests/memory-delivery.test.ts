import { describe, expect, it } from "vitest";
import { createDefaultMessage } from "../src/message.js";
import { MemoryAdapter } from "../src/adapters/memory.js";

const message = (messageId: string) => ({ ...createDefaultMessage(), messageId });

describe("memory delivery", () => {
  it("reserves capacity until acknowledgement and preserves retries", async () => {
    const adapter = new MemoryAdapter(1);
    await adapter.publish("queue", [message("original")]);
    const delivery = await adapter.consume("queue", 1);
    await expect(
      adapter.publish("queue", [message("replacement")]),
    ).rejects.toMatchObject({ code: "QUEUE_FULL" });

    await delivery.items[0]!.receipt.nack();
    const retry = await adapter.consume("queue", 1);
    expect(retry.items.map((item) => item.message.messageId)).toEqual([
      "original",
    ]);
    await retry.items[0]!.receipt.ack();
    await adapter.publish("queue", [message("replacement")]);
  });

  it.each([
    ["ack", "ack"],
    ["ack", "nack"],
    ["nack", "ack"],
    ["nack", "nack"],
  ] as const)("settles once: %s then %s", async (first, second) => {
    const adapter = new MemoryAdapter(1);
    await adapter.publish("queue", [message("original")]);
    const delivery = await adapter.consume("queue", 1);
    const receipt = delivery.items[0]!.receipt;
    await receipt[first]();
    await expect(receipt[second]()).rejects.toMatchObject({
      code: first === "ack" ? "ALREADY_ACKNOWLEDGED" : "ALREADY_NACKED",
    });
    expect(adapter.queueDepth("queue")).toBe(first === "ack" ? 0 : 1);
    const next = adapter.publish("queue", [message("next")]);
    if (first === "ack") await expect(next).resolves.toBeUndefined();
    else await expect(next).rejects.toMatchObject({ code: "QUEUE_FULL" });
  });

  it.each(["ack", "nack"] as const)(
    "clear preserves outstanding receipts: %s",
    async (settlement) => {
      const adapter = new MemoryAdapter(2);
      await adapter.publish("queue", [message("in-flight"), message("queued")]);
      const delivery = await adapter.consume("queue", 1);
      adapter.clear();
      await adapter.publish("queue", [message("new")]);
      await expect(
        adapter.publish("queue", [message("overflow")]),
      ).rejects.toMatchObject({ code: "QUEUE_FULL" });
      await delivery.items[0]!.receipt[settlement]();
      if (settlement === "ack") {
        await adapter.publish("queue", [message("after-ack")]);
      } else {
        const retried = await adapter.consume("queue", 2);
        expect(retried.items.map((item) => item.message.messageId)).toEqual([
          "new",
          "in-flight",
        ]);
      }
    },
  );

  it("batches respect in-flight capacity and queue isolation", async () => {
    const adapter = new MemoryAdapter(3);
    await adapter.publish("queue", [message("a"), message("b")]);
    const delivery = await adapter.consume("queue", 2);
    await expect(
      adapter.publish("queue", [message("c"), message("d")]),
    ).rejects.toMatchObject({ code: "QUEUE_FULL" });
    expect(adapter.queueDepth("queue")).toBe(0);
    await adapter.publish("other", [message("other")]);
    await adapter.publish("queue", [message("c")]);
    for (const item of delivery.items) await item.receipt.nack();
    expect(adapter.queueDepth("queue")).toBe(3);
    expect(adapter.queueDepth("other")).toBe(1);
  });
});

it("cancelled receive leaves the message queued", async () => {
  const adapter = new MemoryAdapter(1);
  await adapter.publish("queue", [message("original")]);
  const controller = new AbortController();
  controller.abort();
  await expect(adapter.consume("queue", 1, controller.signal)).rejects.toBe(controller.signal.reason);
  const delivery = await adapter.consume("queue", 1);
  expect(delivery.items.map((item) => item.message.messageId)).toEqual(["original"]);
});
