// pattern: Imperative Shell

import { randomUUID } from "node:crypto";
import { GrpcqError, validateQueueName, validateTopicAction } from "./error.js";
import { MAX_MESSAGE_SIZE, type Message } from "./message.js";
import type { QueueAdapter } from "./types.js";

export interface MessageSpec {
  topic: string;
  action: string;
  payload: Uint8Array;
  metadata?: Record<string, string>;
}

export class Producer {
  constructor(
    private readonly adapter: QueueAdapter,
    private readonly originator: string,
  ) {}

  async send(
    queueName: string,
    topic: string,
    action: string,
    payload: Uint8Array,
    metadata: Record<string, string> = {},
  ): Promise<void> {
    validateQueueName(queueName);
    validateTopicAction(topic, action);

    if (payload.length > MAX_MESSAGE_SIZE) {
      throw GrpcqError.messageTooLarge(
        topic,
        action,
        MAX_MESSAGE_SIZE,
        payload.length,
      );
    }

    const message: Message = {
      originator: this.originator,
      topic,
      action,
      payload,
      messageId: randomUUID(),
      timestampMs: Date.now(),
      metadata: { ...metadata },
    };

    await this.adapter.publish(queueName, [message]);
  }

  async sendBatch(queueName: string, specs: MessageSpec[]): Promise<void> {
    validateQueueName(queueName);

    const messages: Message[] = [];
    for (const spec of specs) {
      validateTopicAction(spec.topic, spec.action);

      if (spec.payload.length > MAX_MESSAGE_SIZE) {
        throw GrpcqError.messageTooLarge(
          spec.topic,
          spec.action,
          MAX_MESSAGE_SIZE,
          spec.payload.length,
        );
      }

      messages.push({
        originator: this.originator,
        topic: spec.topic,
        action: spec.action,
        payload: spec.payload,
        messageId: randomUUID(),
        timestampMs: Date.now(),
        metadata: { ...spec.metadata },
      });
    }

    await this.adapter.publish(queueName, messages);
  }
}
