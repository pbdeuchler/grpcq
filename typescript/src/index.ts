// Core types
export type {
  QueueAdapter,
  Receipt,
  ConsumeResult,
  MessageItem,
  Handler,
  HandlerContext,
} from "./types.js";

// Message
export {
  type Message,
  MAX_MESSAGE_SIZE,
  encodeMessage,
  decodeMessage,
  createDefaultMessage,
} from "./message.js";

// Errors and validation
export {
  GrpcqError,
  type GrpcqErrorCode,
  validateQueueName,
  validateTopicAction,
} from "./error.js";

// Producer
export { Producer, type MessageSpec } from "./producer.js";

// Registry
export { Registry } from "./registry.js";

// Worker
export { Worker, WorkerPool, type WorkerConfig } from "./worker.js";

// Server
export { Server, type ServerConfig } from "./server.js";

// Client
export { Client, type ClientConfig, type CallOptions } from "./client.js";

// Adapters (also available via "grpcq/adapters" import)
export * as adapters from "./adapters/index.js";
