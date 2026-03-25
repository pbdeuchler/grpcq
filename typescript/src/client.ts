// pattern: Imperative Shell

import { Producer, type MessageSpec } from "./producer.js";
import type { QueueAdapter } from "./types.js";

export interface ClientConfig {
  queueName?: string;
  originator?: string;
}

export interface CallOptions {
  queueName?: string;
  metadata?: Record<string, string>;
}

export class Client {
  private readonly producer: Producer;
  private readonly defaultQueueName: string;

  constructor(adapter: QueueAdapter, config: ClientConfig = {}) {
    this.defaultQueueName = config.queueName ?? "default-queue";
    this.producer = new Producer(
      adapter,
      config.originator ?? "grpcq-client",
    );
  }

  async invoke(
    serviceName: string,
    methodName: string,
    payload: Uint8Array,
    options: CallOptions = {},
  ): Promise<void> {
    const queueName = options.queueName ?? this.defaultQueueName;
    await this.producer.send(
      queueName,
      serviceName,
      methodName,
      payload,
      options.metadata,
    );
  }

  async invokeSpec(queueName: string, spec: MessageSpec): Promise<void> {
    await this.producer.sendBatch(queueName, [spec]);
  }
}
