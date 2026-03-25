// pattern: Functional Core

export type GrpcqErrorCode =
  | "EMPTY_QUEUE_NAME"
  | "QUEUE_NAME_WHITESPACE"
  | "INVALID_QUEUE_NAME_CHARACTER"
  | "EMPTY_TOPIC"
  | "EMPTY_ACTION"
  | "TOPIC_WHITESPACE"
  | "ACTION_WHITESPACE"
  | "MESSAGE_TOO_LARGE"
  | "UNKNOWN_TOPIC"
  | "UNKNOWN_ACTION"
  | "REQUEST_DECODE"
  | "ALREADY_ACKNOWLEDGED"
  | "ALREADY_NACKED"
  | "QUEUE_FULL"
  | "CONSUME_NOT_SUPPORTED"
  | "SERVER_NOT_STARTED"
  | "WORKER_ALREADY_STARTED"
  | "WORKER_POOL_ALREADY_STARTED"
  | "CANCELLED"
  | "OTHER";

export class GrpcqError extends Error {
  constructor(
    public readonly code: GrpcqErrorCode,
    message: string,
    public readonly cause?: unknown,
  ) {
    super(message);
    this.name = "GrpcqError";
  }

  static emptyQueueName(): GrpcqError {
    return new GrpcqError("EMPTY_QUEUE_NAME", "queue name cannot be empty");
  }

  static queueNameWhitespace(): GrpcqError {
    return new GrpcqError(
      "QUEUE_NAME_WHITESPACE",
      "queue name cannot have leading or trailing whitespace",
    );
  }

  static invalidQueueNameCharacter(char: string): GrpcqError {
    return new GrpcqError(
      "INVALID_QUEUE_NAME_CHARACTER",
      `queue name contains invalid character: ${char}`,
    );
  }

  static emptyTopic(): GrpcqError {
    return new GrpcqError("EMPTY_TOPIC", "topic cannot be empty");
  }

  static emptyAction(): GrpcqError {
    return new GrpcqError("EMPTY_ACTION", "action cannot be empty");
  }

  static topicWhitespace(): GrpcqError {
    return new GrpcqError(
      "TOPIC_WHITESPACE",
      "topic cannot have leading or trailing whitespace",
    );
  }

  static actionWhitespace(): GrpcqError {
    return new GrpcqError(
      "ACTION_WHITESPACE",
      "action cannot have leading or trailing whitespace",
    );
  }

  static messageTooLarge(
    topic: string,
    action: string,
    limit: number,
    actual: number,
  ): GrpcqError {
    return new GrpcqError(
      "MESSAGE_TOO_LARGE",
      `message payload for ${topic}.${action} exceeds maximum size of ${limit} bytes (got ${actual} bytes)`,
    );
  }

  static unknownTopic(topic: string): GrpcqError {
    return new GrpcqError(
      "UNKNOWN_TOPIC",
      `no handlers registered for topic: ${topic}`,
    );
  }

  static unknownAction(topic: string, action: string): GrpcqError {
    return new GrpcqError(
      "UNKNOWN_ACTION",
      `no handler registered for topic: ${topic}, action: ${action}`,
    );
  }

  static requestDecode(
    service: string,
    method: string,
    cause: unknown,
  ): GrpcqError {
    return new GrpcqError(
      "REQUEST_DECODE",
      `failed to decode request for ${service}.${method}: ${cause}`,
      cause,
    );
  }

  static alreadyAcknowledged(): GrpcqError {
    return new GrpcqError(
      "ALREADY_ACKNOWLEDGED",
      "message already acknowledged",
    );
  }

  static alreadyNacked(): GrpcqError {
    return new GrpcqError("ALREADY_NACKED", "message already nacked");
  }

  static queueFull(queueName: string): GrpcqError {
    return new GrpcqError("QUEUE_FULL", `queue ${queueName} is full`);
  }

  static serverNotStarted(): GrpcqError {
    return new GrpcqError("SERVER_NOT_STARTED", "server not started");
  }

  static workerAlreadyStarted(): GrpcqError {
    return new GrpcqError(
      "WORKER_ALREADY_STARTED",
      "worker has already been started",
    );
  }

  static workerPoolAlreadyStarted(): GrpcqError {
    return new GrpcqError(
      "WORKER_POOL_ALREADY_STARTED",
      "worker pool has already been started",
    );
  }

  static cancelled(): GrpcqError {
    return new GrpcqError("CANCELLED", "operation cancelled");
  }

  static other(message: string, cause?: unknown): GrpcqError {
    return new GrpcqError("OTHER", message, cause);
  }
}

const VALID_QUEUE_NAME_RE = /^[a-zA-Z0-9\-_.]+$/;

export function validateQueueName(queueName: string): void {
  if (!queueName) {
    throw GrpcqError.emptyQueueName();
  }
  if (queueName !== queueName.trim()) {
    throw GrpcqError.queueNameWhitespace();
  }
  if (!VALID_QUEUE_NAME_RE.test(queueName)) {
    const invalid = [...queueName].find(
      (ch) => !/[a-zA-Z0-9\-_.]/.test(ch),
    );
    throw GrpcqError.invalidQueueNameCharacter(invalid ?? queueName);
  }
}

export function validateTopicAction(topic: string, action: string): void {
  if (!topic) {
    throw GrpcqError.emptyTopic();
  }
  if (!action) {
    throw GrpcqError.emptyAction();
  }
  if (topic !== topic.trim()) {
    throw GrpcqError.topicWhitespace();
  }
  if (action !== action.trim()) {
    throw GrpcqError.actionWhitespace();
  }
}
