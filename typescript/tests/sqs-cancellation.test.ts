import { expect, it } from "vitest";
import { createDefaultMessage } from "../src/message.js";
import { SqsAdapter, encodeMessageBody, type SqsClient } from "../src/adapters/sqs.js";

class Command {
  constructor(readonly input: unknown) {}
}
const commands = {
  SendMessageBatchCommand: Command,
  ReceiveMessageCommand: Command,
  DeleteMessageCommand: Command,
  ChangeMessageVisibilityCommand: Command,
};

it("forwards cancellation to an SQS long poll", async () => {
  const controller = new AbortController();
  const client: SqsClient = {
    send: (_command, options) => {
      expect(options?.abortSignal).toBe(controller.signal);
      return new Promise((_, reject) => {
        options!.abortSignal!.addEventListener(
          "abort", () => reject(options!.abortSignal!.reason), { once: true },
        );
      });
    },
  };
  const adapter = new SqsAdapter({ client, queueUrls: { q: "queue-url" } }, commands);
  const receiving = adapter.consume("q", 1, controller.signal);
  controller.abort();
  await expect(receiving).rejects.toBe(controller.signal.reason);
});

it.each(["ack", "nack"] as const)("intake cancellation does not cancel %s", async settlement => {
  const controller = new AbortController();
  const options: Array<{ abortSignal?: AbortSignal } | undefined> = [];
  const client: SqsClient = {
    send: async (_command, callOptions) => {
      options.push(callOptions);
      if (options.length === 1) {
        return { Messages: [{ Body: encodeMessageBody(createDefaultMessage()), ReceiptHandle: "receipt" }] };
      }
      return {};
    },
  };
  const adapter = new SqsAdapter({ client, queueUrls: { q: "queue-url" } }, commands);
  const delivery = await adapter.consume("q", 1, controller.signal);
  controller.abort();
  await delivery.items[0]!.receipt[settlement]();
  expect(options).toEqual([{ abortSignal: controller.signal }, undefined]);
});

it.each([
  ["ack", "ack"], ["ack", "nack"], ["nack", "ack"], ["nack", "nack"],
] as const)("a failed %s leaves the SQS receipt usable for %s", async (first, second) => {
  let calls = 0;
  const failure = new Error("SQS unavailable");
  const client: SqsClient = {
    send: async () => {
      if (calls++ === 0) return { Messages: [{ Body: encodeMessageBody(createDefaultMessage()), ReceiptHandle: "receipt" }] };
      if (calls === 2) throw failure;
      return {};
    },
  };
  const adapter = new SqsAdapter({ client, queueUrls: { q: "queue-url" } }, commands);
  const delivery = await adapter.consume("q", 1);
  const receipt = delivery.items[0]!.receipt;
  await expect(receipt[first]()).rejects.toMatchObject({ cause: failure });
  await expect(receipt[second]()).resolves.toBeUndefined();
  expect(calls).toBe(3);
});
