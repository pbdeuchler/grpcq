// pattern: Imperative Shell

import { GrpcqError } from "../error.js";
import type { Message } from "../message.js";
import type {
  ConsumeResult,
  MessageItem,
  QueueAdapter,
  Receipt,
} from "../types.js";

const enum ReceiptState {
  Pending = 0,
  Acked = 1,
  Nacked = 2,
}

export class MemoryAdapter implements QueueAdapter {
  private readonly queues = new Map<string, Message[]>();
  private readonly bufferSize: number;

  constructor(bufferSize = 1000) {
    this.bufferSize = bufferSize > 0 ? bufferSize : 1000;
  }

  async publish(queueName: string, messages: Message[]): Promise<void> {
    if (messages.length === 0) return;

    const queue = this.getOrCreateQueue(queueName);
    if (queue.length + messages.length > this.bufferSize) {
      throw GrpcqError.queueFull(queueName);
    }

    queue.push(...messages.map((m) => structuredClone(m)));
  }

  async consume(queueName: string, maxBatch: number): Promise<ConsumeResult> {
    const queue = this.getOrCreateQueue(queueName);
    const limit = Math.max(maxBatch, 1);
    const drained = queue.splice(0, limit);

    const items: MessageItem[] = drained.map((message) => ({
      message,
      receipt: this.createReceipt(queueName, message),
    }));

    return { items };
  }

  queueDepth(queueName: string): number {
    return this.queues.get(queueName)?.length ?? 0;
  }

  clear(): void {
    for (const queue of this.queues.values()) {
      queue.length = 0;
    }
  }

  private getOrCreateQueue(queueName: string): Message[] {
    let queue = this.queues.get(queueName);
    if (!queue) {
      queue = [];
      this.queues.set(queueName, queue);
    }
    return queue;
  }

  private createReceipt(queueName: string, message: Message): Receipt {
    let state = ReceiptState.Pending;

    return {
      ack: async () => {
        if (state === ReceiptState.Acked)
          throw GrpcqError.alreadyAcknowledged();
        if (state === ReceiptState.Nacked) throw GrpcqError.alreadyNacked();
        state = ReceiptState.Acked;
      },
      nack: async () => {
        if (state === ReceiptState.Acked)
          throw GrpcqError.alreadyAcknowledged();
        if (state === ReceiptState.Nacked) throw GrpcqError.alreadyNacked();
        state = ReceiptState.Nacked;

        const queue = this.getOrCreateQueue(queueName);
        if (queue.length < this.bufferSize) {
          queue.push(structuredClone(message));
        }
      },
    };
  }
}
