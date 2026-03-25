package sqs

import (
	"testing"

	pb "github.com/pbdeuchler/grpcq/go/proto"
	"google.golang.org/protobuf/proto"
)

func TestMessageBodyRoundTripUsesTextSafeEncoding(t *testing.T) {
	original := &pb.Message{
		Originator: "svc",
		Topic:      "topic",
		Action:     "action",
		MessageId:  "id-1",
		Payload:    []byte{0x00, 0xff, 0x10, 0x80},
	}

	body, err := encodeMessageBody(original)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := decodeMessageBody(body)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if string(decoded.Payload) != string(original.Payload) {
		t.Fatalf("payload mismatch: got %v want %v", decoded.Payload, original.Payload)
	}
}

func TestDecodeMessageBodySupportsLegacyRawPayloads(t *testing.T) {
	original := &pb.Message{
		Originator: "svc",
		Topic:      "topic",
		Action:     "action",
		MessageId:  "legacy",
		Payload:    []byte{0x01, 0x02, 0x03},
	}

	raw, err := proto.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	decoded, err := decodeMessageBody(string(raw))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.MessageId != original.MessageId {
		t.Fatalf("unexpected decoded message: %+v", decoded)
	}
}
