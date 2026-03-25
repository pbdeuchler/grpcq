// pattern: Functional Core

import { GrpcqError } from "./error.js";
import type { Message } from "./message.js";
import type { Handler } from "./types.js";

export class Registry {
  private readonly handlers = new Map<string, Map<string, Handler>>();

  register(topic: string, action: string, handler: Handler): void {
    let topicHandlers = this.handlers.get(topic);
    if (!topicHandlers) {
      topicHandlers = new Map();
      this.handlers.set(topic, topicHandlers);
    }
    topicHandlers.set(action, handler);
  }

  async handle(message: Message): Promise<void> {
    const topicHandlers = this.handlers.get(message.topic);
    if (!topicHandlers) {
      throw GrpcqError.unknownTopic(message.topic);
    }

    const handler = topicHandlers.get(message.action);
    if (!handler) {
      throw GrpcqError.unknownAction(message.topic, message.action);
    }

    await handler(message);
  }

  isRegistered(topic: string, action: string): boolean {
    return this.handlers.get(topic)?.has(action) ?? false;
  }

  topics(): string[] {
    return [...this.handlers.keys()];
  }

  actions(topic: string): string[] | undefined {
    const topicHandlers = this.handlers.get(topic);
    if (!topicHandlers) return undefined;
    return [...topicHandlers.keys()];
  }
}
