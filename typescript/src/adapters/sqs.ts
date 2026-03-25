// pattern: Imperative Shell (adapter I/O) + Functional Core (encode/decode)

import { GrpcqError } from "../error.js";
import {
  type Message,
  decodeMessage,
  encodeMessage,
} from "../message.js";
import type {
  ConsumeResult,
  MessageItem,
  QueueAdapter,
  Receipt,
} from "../types.js";

const MAX_BATCH_SIZE = 10;
const LONG_POLL_SECONDS = 20;

export interface SqsAdapterConfig {
  client: SqsClient;
  queueUrls: Record<string, string>;
}

// Minimal SQS client interface — matches @aws-sdk/client-sqs shapes
// without requiring the dependency at compile time.
export interface SqsClient {
  send(command: unknown): Promise<unknown>;
}

// Re-usable command constructors. Users pass an actual @aws-sdk/client-sqs
// Client; we construct command objects matching its expected shapes.
interface SqsSendMessageBatchCommand {
  new (input: {
    QueueUrl: string;
    Entries: SqsBatchEntry[];
  }): unknown;
}

interface SqsReceiveMessageCommand {
  new (input: {
    QueueUrl: string;
    MaxNumberOfMessages: number;
    WaitTimeSeconds: number;
    MessageAttributeNames: string[];
  }): unknown;
}

interface SqsDeleteMessageCommand {
  new (input: { QueueUrl: string; ReceiptHandle: string }): unknown;
}

interface SqsChangeMessageVisibilityCommand {
  new (input: {
    QueueUrl: string;
    ReceiptHandle: string;
    VisibilityTimeout: number;
  }): unknown;
}

interface SqsBatchEntry {
  Id: string;
  MessageBody: string;
  MessageAttributes?: Record<
    string,
    { DataType: string; StringValue: string }
  >;
}

interface SqsReceiveResult {
  Messages?: Array<{
    Body?: string;
    ReceiptHandle?: string;
    MessageAttributes?: Record<
      string,
      { DataType?: string; StringValue?: string }
    >;
  }>;
}

interface SqsSendBatchResult {
  Failed?: Array<{ Message?: string }>;
}

// -- Functional Core: encode/decode --

export function encodeMessageBody(msg: Message): string {
  const bytes = encodeMessage(msg);
  return Buffer.from(bytes).toString("base64");
}

export function decodeMessageBody(body: string): Message {
  // Try base64 decode first
  try {
    const bytes = Buffer.from(body, "base64");
    // Verify it's actually valid base64 by re-encoding
    if (Buffer.from(bytes).toString("base64") === body || body === "") {
      return decodeMessage(new Uint8Array(bytes));
    }
  } catch {
    // Fall through to raw protobuf attempt
  }

  // Fallback: raw protobuf (legacy compat with Go producer)
  try {
    return decodeMessage(new TextEncoder().encode(body));
  } catch (err) {
    throw GrpcqError.other(`failed to decode message body: ${err}`, err);
  }
}

// -- Adapter --

export class SqsAdapter implements QueueAdapter {
  private readonly client: SqsClient;
  private readonly queueUrls: Record<string, string>;

  // Command constructors — injected to avoid hard @aws-sdk dependency
  private readonly SendMessageBatchCommand: SqsSendMessageBatchCommand;
  private readonly ReceiveMessageCommand: SqsReceiveMessageCommand;
  private readonly DeleteMessageCommand: SqsDeleteMessageCommand;
  private readonly ChangeMessageVisibilityCommand: SqsChangeMessageVisibilityCommand;

  constructor(
    config: SqsAdapterConfig,
    commands: {
      SendMessageBatchCommand: SqsSendMessageBatchCommand;
      ReceiveMessageCommand: SqsReceiveMessageCommand;
      DeleteMessageCommand: SqsDeleteMessageCommand;
      ChangeMessageVisibilityCommand: SqsChangeMessageVisibilityCommand;
    },
  ) {
    if (Object.keys(config.queueUrls).length === 0) {
      throw GrpcqError.other("at least one queue URL is required");
    }
    this.client = config.client;
    this.queueUrls = { ...config.queueUrls };
    this.SendMessageBatchCommand = commands.SendMessageBatchCommand;
    this.ReceiveMessageCommand = commands.ReceiveMessageCommand;
    this.DeleteMessageCommand = commands.DeleteMessageCommand;
    this.ChangeMessageVisibilityCommand =
      commands.ChangeMessageVisibilityCommand;
  }

  async publish(queueName: string, messages: Message[]): Promise<void> {
    if (messages.length === 0) return;

    const queueUrl = this.resolveUrl(queueName);

    // Batch in groups of 10 (SQS limit)
    for (let i = 0; i < messages.length; i += MAX_BATCH_SIZE) {
      const chunk = messages.slice(i, i + MAX_BATCH_SIZE);
      await this.sendBatch(queueUrl, chunk);
    }
  }

  async consume(
    queueName: string,
    maxBatch: number,
  ): Promise<ConsumeResult> {
    const queueUrl = this.resolveUrl(queueName);
    const clamped = Math.min(Math.max(maxBatch, 1), MAX_BATCH_SIZE);

    const output = (await this.client.send(
      new this.ReceiveMessageCommand({
        QueueUrl: queueUrl,
        MaxNumberOfMessages: clamped,
        WaitTimeSeconds: LONG_POLL_SECONDS,
        MessageAttributeNames: ["All"],
      }),
    )) as SqsReceiveResult;

    const items: MessageItem[] = (output.Messages ?? []).map((sqsMsg) => {
      const message = decodeMessageBody(sqsMsg.Body ?? "");
      const receipt = this.createReceipt(queueUrl, sqsMsg.ReceiptHandle ?? "");
      return { message, receipt };
    });

    return { items };
  }

  private resolveUrl(queueName: string): string {
    const url = this.queueUrls[queueName];
    if (!url) {
      throw GrpcqError.other(`queue name ${queueName} not configured`);
    }
    return url;
  }

  private async sendBatch(
    queueUrl: string,
    messages: Message[],
  ): Promise<void> {
    const entries: SqsBatchEntry[] = messages.map((msg) => ({
      Id: msg.messageId,
      MessageBody: encodeMessageBody(msg),
      MessageAttributes: {
        topic: { DataType: "String", StringValue: msg.topic },
        action: { DataType: "String", StringValue: msg.action },
        originator: { DataType: "String", StringValue: msg.originator },
      },
    }));

    const output = (await this.client.send(
      new this.SendMessageBatchCommand({
        QueueUrl: queueUrl,
        Entries: entries,
      }),
    )) as SqsSendBatchResult;

    const failed = output.Failed ?? [];
    if (failed.length > 0) {
      const msg = failed[0]?.Message ?? "unknown error";
      throw GrpcqError.other(
        `failed to send ${failed.length} message(s): ${msg}`,
      );
    }
  }

  private createReceipt(queueUrl: string, receiptHandle: string): Receipt {
    let state: "pending" | "acked" | "nacked" = "pending";

    return {
      ack: async () => {
        if (state === "acked") throw GrpcqError.alreadyAcknowledged();
        if (state === "nacked") throw GrpcqError.alreadyNacked();
        state = "acked";

        try {
          await this.client.send(
            new this.DeleteMessageCommand({
              QueueUrl: queueUrl,
              ReceiptHandle: receiptHandle,
            }),
          );
        } catch (err) {
          throw GrpcqError.other(
            `failed to delete message from SQS: ${err}`,
            err,
          );
        }
      },
      nack: async () => {
        if (state === "acked") throw GrpcqError.alreadyAcknowledged();
        if (state === "nacked") throw GrpcqError.alreadyNacked();
        state = "nacked";

        try {
          await this.client.send(
            new this.ChangeMessageVisibilityCommand({
              QueueUrl: queueUrl,
              ReceiptHandle: receiptHandle,
              VisibilityTimeout: 0,
            }),
          );
        } catch (err) {
          throw GrpcqError.other(
            `failed to change visibility in SQS: ${err}`,
            err,
          );
        }
      },
    };
  }
}
