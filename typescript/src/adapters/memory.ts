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

interface Queue {
  messages: Message[];
  pending: number;
}

export class MemoryAdapter implements QueueAdapter {
  private readonly queues = new Map<string, Queue>();
  private readonly bufferSize: number;

  /** Capacity includes queued and in-flight messages until acknowledgement. */
  constructor(bufferSize = 1000) {
    this.bufferSize = bufferSize > 0 ? bufferSize : 1000;
  }

  async publish(queueName: string, messages: Message[]): Promise<void> {
    if (messages.length === 0) return;

    const queue = this.getOrCreateQueue(queueName);
    if (messages.length > this.bufferSize - queue.pending) {
      throw GrpcqError.queueFull(queueName);
    }

    queue.messages.push(...messages.map((m) => structuredClone(m)));
    queue.pending += messages.length;
  }

  async consume(
    queueName: string,
    maxBatch: number,
    signal?: AbortSignal,
  ): Promise<ConsumeResult> {
    signal?.throwIfAborted();
    const queue = this.getOrCreateQueue(queueName);
    const limit = Math.max(maxBatch, 1);
    const drained = queue.messages.splice(0, limit);

    const items: MessageItem[] = drained.map((message) => ({
      message,
      receipt: this.createReceipt(queue, message),
    }));

    return { items };
  }

  queueDepth(queueName: string): number {
    return this.queues.get(queueName)?.messages.length ?? 0;
  }

  /** Discard queued messages, preserving outstanding receipts and their capacity. */
  clear(): void {
    for (const queue of this.queues.values()) {
      queue.pending -= queue.messages.length;
      queue.messages.length = 0;
    }
  }

  private getOrCreateQueue(queueName: string): Queue {
    let queue = this.queues.get(queueName);
    if (!queue) {
      queue = { messages: [], pending: 0 };
      this.queues.set(queueName, queue);
    }
    return queue;
  }

  private createReceipt(queue: Queue, message: Message): Receipt {
    let state = ReceiptState.Pending;

    return {
      ack: async () => {
        if (state === ReceiptState.Acked)
          throw GrpcqError.alreadyAcknowledged();
        if (state === ReceiptState.Nacked) throw GrpcqError.alreadyNacked();
        state = ReceiptState.Acked;
        queue.pending--;
      },
      nack: async () => {
        if (state === ReceiptState.Acked)
          throw GrpcqError.alreadyAcknowledged();
        if (state === ReceiptState.Nacked) throw GrpcqError.alreadyNacked();
        queue.messages.push(structuredClone(message));
        state = ReceiptState.Nacked;
      },
    };
  }
}
