// pattern: Functional Core

export const MAX_MESSAGE_SIZE = 256 * 1024;

export interface Message {
  originator: string;
  topic: string;
  action: string;
  payload: Uint8Array;
  messageId: string;
  timestampMs: number;
  metadata: Record<string, string>;
}

export function createDefaultMessage(): Message {
  return {
    originator: "",
    topic: "",
    action: "",
    payload: new Uint8Array(0),
    messageId: "",
    timestampMs: 0,
    metadata: {},
  };
}

// -- Minimal protobuf codec for the Message wire format --
// Field numbers match proto/message.proto:
//   1=originator, 2=topic, 3=action, 4=payload,
//   5=message_id, 6=timestamp_ms, 7=metadata (map<string,string>)

const WIRE_VARINT = 0;
const WIRE_LENGTH_DELIMITED = 2;

function encodeVarint(value: number): Uint8Array {
  const bytes: number[] = [];
  let v = value;
  while (v > 0x7f) {
    bytes.push((v % 128) | 0x80);
    v = Math.floor(v / 128);
  }
  bytes.push(v);
  return new Uint8Array(bytes);
}

function encodeTag(fieldNumber: number, wireType: number): Uint8Array {
  return encodeVarint(fieldNumber * 8 + wireType);
}

function encodeLengthDelimited(
  fieldNumber: number,
  data: Uint8Array,
): Uint8Array {
  if (data.length === 0) return new Uint8Array(0);
  const tag = encodeTag(fieldNumber, WIRE_LENGTH_DELIMITED);
  const length = encodeVarint(data.length);
  const result = new Uint8Array(tag.length + length.length + data.length);
  result.set(tag, 0);
  result.set(length, tag.length);
  result.set(data, tag.length + length.length);
  return result;
}

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

function encodeStringField(fieldNumber: number, value: string): Uint8Array {
  if (!value) return new Uint8Array(0);
  return encodeLengthDelimited(fieldNumber, textEncoder.encode(value));
}

function encodeMapEntry(key: string, value: string): Uint8Array {
  const keyField = encodeStringField(1, key);
  const valueField = encodeStringField(2, value);
  const entryData = new Uint8Array(keyField.length + valueField.length);
  entryData.set(keyField, 0);
  entryData.set(valueField, keyField.length);
  return encodeLengthDelimited(7, entryData);
}

function concat(chunks: Uint8Array[]): Uint8Array {
  let totalLength = 0;
  for (const chunk of chunks) totalLength += chunk.length;
  const result = new Uint8Array(totalLength);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.length;
  }
  return result;
}

export function encodeMessage(msg: Message): Uint8Array {
  const parts: Uint8Array[] = [];

  parts.push(encodeStringField(1, msg.originator));
  parts.push(encodeStringField(2, msg.topic));
  parts.push(encodeStringField(3, msg.action));

  if (msg.payload.length > 0) {
    parts.push(encodeLengthDelimited(4, msg.payload));
  }

  parts.push(encodeStringField(5, msg.messageId));

  if (msg.timestampMs !== 0) {
    const tag = encodeTag(6, WIRE_VARINT);
    const value = encodeVarint(msg.timestampMs);
    const field = new Uint8Array(tag.length + value.length);
    field.set(tag, 0);
    field.set(value, tag.length);
    parts.push(field);
  }

  for (const [key, value] of Object.entries(msg.metadata)) {
    parts.push(encodeMapEntry(key, value));
  }

  return concat(parts.filter((p) => p.length > 0));
}

class ProtoReader {
  private pos = 0;

  constructor(private readonly data: Uint8Array) {}

  get remaining(): number {
    return this.data.length - this.pos;
  }

  readVarint(): number {
    let result = 0;
    let shift = 0;
    while (this.pos < this.data.length) {
      const byte = this.data[this.pos++]!;
      result += (byte & 0x7f) * 2 ** shift;
      if ((byte & 0x80) === 0) return result;
      shift += 7;
      if (shift > 49) throw new Error("varint too long");
    }
    throw new Error("unexpected end of data");
  }

  readTag(): { fieldNumber: number; wireType: number } | null {
    if (this.remaining <= 0) return null;
    const tag = this.readVarint();
    return { fieldNumber: Math.floor(tag / 8), wireType: tag % 8 };
  }

  readBytes(): Uint8Array {
    const length = this.readVarint();
    if (this.pos + length > this.data.length) {
      throw new Error("unexpected end of data");
    }
    const result = this.data.slice(this.pos, this.pos + length);
    this.pos += length;
    return result;
  }

  readString(): string {
    return textDecoder.decode(this.readBytes());
  }

  skipField(wireType: number): void {
    switch (wireType) {
      case WIRE_VARINT:
        this.readVarint();
        break;
      case WIRE_LENGTH_DELIMITED:
        this.readBytes();
        break;
      default:
        throw new Error(`unsupported wire type: ${wireType}`);
    }
  }
}

function decodeMapEntry(data: Uint8Array): [string, string] {
  const reader = new ProtoReader(data);
  let key = "";
  let value = "";
  for (;;) {
    const tag = reader.readTag();
    if (!tag) break;
    switch (tag.fieldNumber) {
      case 1:
        key = reader.readString();
        break;
      case 2:
        value = reader.readString();
        break;
      default:
        reader.skipField(tag.wireType);
    }
  }
  return [key, value];
}

export function decodeMessage(data: Uint8Array): Message {
  const msg = createDefaultMessage();
  const reader = new ProtoReader(data);

  for (;;) {
    const tag = reader.readTag();
    if (!tag) break;

    switch (tag.fieldNumber) {
      case 1:
        msg.originator = reader.readString();
        break;
      case 2:
        msg.topic = reader.readString();
        break;
      case 3:
        msg.action = reader.readString();
        break;
      case 4:
        msg.payload = reader.readBytes();
        break;
      case 5:
        msg.messageId = reader.readString();
        break;
      case 6:
        msg.timestampMs = reader.readVarint();
        break;
      case 7:
        {
          const entryData = reader.readBytes();
          const [key, value] = decodeMapEntry(entryData);
          msg.metadata[key] = value;
        }
        break;
      default:
        reader.skipField(tag.wireType);
    }
  }

  return msg;
}
