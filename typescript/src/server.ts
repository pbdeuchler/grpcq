// pattern: Imperative Shell

import { GrpcqError } from "./error.js";
import type { Message } from "./message.js";
import { Registry } from "./registry.js";
import type { HandlerContext, QueueAdapter } from "./types.js";
import { Worker } from "./worker.js";

export interface ServerConfig {
  queueName?: string;
  concurrency?: number;
  maxBatch?: number;
  pollIntervalMs?: number;
}

export class Server {
  private readonly registry: Registry;
  private readonly worker: Worker;
  private started = false;

  constructor(adapter: QueueAdapter, config: ServerConfig = {}) {
    this.registry = new Registry();
    this.worker = new Worker(adapter, this.registry, {
      queueName: config.queueName ?? "default-queue",
      concurrency: config.concurrency ?? 10,
      maxBatch: config.maxBatch ?? 10,
      pollIntervalMs: config.pollIntervalMs ?? 1000,
    });
  }

  registerMethod<TReq, TResp>(
    serviceName: string,
    methodName: string,
    decoder: (data: Uint8Array) => TReq,
    handler: (ctx: HandlerContext, request: TReq) => Promise<TResp>,
  ): void {
    this.registry.register(
      serviceName,
      methodName,
      async (message: Message): Promise<void> => {
        let request: TReq;
        try {
          request = decoder(message.payload);
        } catch (err) {
          throw GrpcqError.requestDecode(serviceName, methodName, err);
        }

        const ctx: HandlerContext = { message };
        await handler(ctx, request);
      },
    );
  }

  async start(signal?: AbortSignal): Promise<void> {
    this.started = true;
    await this.worker.start(signal);
  }

  async stop(): Promise<void> {
    if (!this.started) {
      throw GrpcqError.serverNotStarted();
    }
    await this.worker.stop();
  }
}
