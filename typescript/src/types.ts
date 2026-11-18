// pattern: Functional Core

import type { Message } from "./message.js";

export interface QueueAdapter {
  publish(queueName: string, messages: Message[]): Promise<void>;
  /** Abort pending I/O when signalled. A returned batch transfers ownership to the worker. */
  consume(
    queueName: string,
    maxBatch: number,
    signal?: AbortSignal,
  ): Promise<ConsumeResult>;
}

export interface Receipt {
  ack(): Promise<void>;
  nack(): Promise<void>;
}

export interface MessageItem {
  message: Message;
  receipt: Receipt;
}

export interface ConsumeResult {
  items: MessageItem[];
}

export type Handler = (message: Message) => Promise<void>;

export interface HandlerContext {
  message: Message;
}
