// Package sqs provides an AWS SQS adapter for grpcq.
package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/pbdeuchler/grpcq/go/core"
	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

// Adapter is an AWS SQS implementation of the QueueAdapter interface.
type Adapter struct {
	client    *sqs.Client
	queueURLs map[string]string // Maps queue names to URLs
}

// Config contains configuration for the SQS adapter.
type Config struct {
	// QueueURLs maps queue names to their full SQS URLs
	// Example: map[string]string{"user-queue": "https://sqs.us-east-1.amazonaws.com/123/user-queue"}
	QueueURLs map[string]string

	// Client is the AWS SQS client to use
	// If nil, a default client will be created
	Client *sqs.Client
}

// NewAdapter creates a new SQS adapter with the given configuration.
func NewAdapter(cfg Config) (*Adapter, error) {
	if len(cfg.QueueURLs) == 0 {
		return nil, fmt.Errorf("at least one queue URL is required")
	}

	if cfg.Client == nil {
		return nil, fmt.Errorf("SQS client is required")
	}

	return &Adapter{
		client:    cfg.Client,
		queueURLs: cfg.QueueURLs,
	}, nil
}

// Publish sends messages to SQS using SendMessageBatch.
// Messages are sent in batches of up to 10 (SQS limit).
func (a *Adapter) Publish(ctx context.Context, queueName string, messages ...*pb.Message) error {
	queueURL, ok := a.queueURLs[queueName]
	if !ok {
		return fmt.Errorf("queue name %s not configured", queueName)
	}

	// SQS SendMessageBatch limit is 10 messages
	const maxBatchSize = 10

	for i := 0; i < len(messages); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(messages) {
			end = len(messages)
		}

		batch := messages[i:end]
		if err := a.publishBatch(ctx, queueURL, batch); err != nil {
			return err
		}
	}

	return nil
}

// publishBatch sends a single batch to SQS.
func (a *Adapter) publishBatch(ctx context.Context, queueURL string, messages []*pb.Message) error {
	entries := make([]types.SendMessageBatchRequestEntry, len(messages))

	for i, msg := range messages {
		// Serialize the message
		data, err := proto.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal message %d: %w", i, err)
		}

		// Create batch entry
		entries[i] = types.SendMessageBatchRequestEntry{
			Id:          aws.String(msg.MessageId),
			MessageBody: aws.String(string(data)),
			MessageAttributes: map[string]types.MessageAttributeValue{
				"topic": {
					DataType:    aws.String("String"),
					StringValue: aws.String(msg.Topic),
				},
				"action": {
					DataType:    aws.String("String"),
					StringValue: aws.String(msg.Action),
				},
				"originator": {
					DataType:    aws.String("String"),
					StringValue: aws.String(msg.Originator),
				},
			},
		}
	}

	// Send the batch
	output, err := a.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: aws.String(queueURL),
		Entries:  entries,
	})

	if err != nil {
		return fmt.Errorf("failed to send batch to SQS: %w", err)
	}

	// Check for failed messages
	if len(output.Failed) > 0 {
		return fmt.Errorf("failed to send %d messages: %s",
			len(output.Failed),
			aws.ToString(output.Failed[0].Message))
	}

	return nil
}

// Consume retrieves messages from SQS using ReceiveMessage.
func (a *Adapter) Consume(ctx context.Context, queueName string, maxBatch int) (*core.ConsumeResult, error) {
	queueURL, ok := a.queueURLs[queueName]
	if !ok {
		return nil, fmt.Errorf("queue name %s not configured", queueName)
	}

	// SQS ReceiveMessage limit is 10 messages
	if maxBatch > 10 {
		maxBatch = 10
	}

	output, err := a.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(queueURL),
		MaxNumberOfMessages:   int32(maxBatch),
		WaitTimeSeconds:       20, // Long polling
		MessageAttributeNames: []string{"All"},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to receive messages from SQS: %w", err)
	}

	items := make([]core.MessageItem, 0, len(output.Messages))

	for _, sqsMsg := range output.Messages {
		// Deserialize the message
		var msg pb.Message
		if err := proto.Unmarshal([]byte(aws.ToString(sqsMsg.Body)), &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		// Create receipt
		receipt := &sqsReceipt{
			client:        a.client,
			queueURL:      queueURL,
			receiptHandle: aws.ToString(sqsMsg.ReceiptHandle),
		}

		items = append(items, core.MessageItem{
			Message: &msg,
			Receipt: receipt,
		})
	}

	return &core.ConsumeResult{Items: items}, nil
}

// sqsReceipt implements the Receipt interface for SQS messages.
type sqsReceipt struct {
	client        *sqs.Client
	queueURL      string
	receiptHandle string
}

// Ack deletes the message from SQS.
func (r *sqsReceipt) Ack(ctx context.Context) error {
	_, err := r.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(r.queueURL),
		ReceiptHandle: aws.String(r.receiptHandle),
	})

	if err != nil {
		return fmt.Errorf("failed to delete message from SQS: %w", err)
	}

	return nil
}

// Nack changes the visibility timeout to 0, making the message immediately available for reprocessing.
func (r *sqsReceipt) Nack(ctx context.Context) error {
	_, err := r.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(r.queueURL),
		ReceiptHandle:     aws.String(r.receiptHandle),
		VisibilityTimeout: 0, // Make immediately available
	})

	if err != nil {
		return fmt.Errorf("failed to change message visibility in SQS: %w", err)
	}

	return nil
}

// Ensure Adapter implements QueueAdapter
var _ core.QueueAdapter = (*Adapter)(nil)
