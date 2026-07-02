// Package protocol defines the wire contract exchanged between client and
// server after the Noise handshake completes.
//
// Every logical message is carried inside a single Noise-sealed record. Because
// a Noise/ChaChaPoly record caps plaintext at 65519 bytes, larger payloads
// (file content) are chunked into a content stream: a JSON envelope announces
// the stream, then a run of binary chunk records delivers the bytes.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// MaxRecordPlaintext is the largest plaintext a single Noise ChaChaPoly record
// can hold (65535 total minus the 16-byte auth tag).
const MaxRecordPlaintext = 65519

// ChunkPayloadSize is the content bytes per chunk record, leaving headroom for
// the frame tag (1) and chunk header (8+4+1 = 13).
const ChunkPayloadSize = MaxRecordPlaintext - 1 - 13

// Frame tags prefix every decrypted record so the receiver can route it.
const (
	tagEnvelope byte = 0x01
	tagChunk    byte = 0x02
)

// Message type constants for Envelope.Type.
const (
	// Client -> Server
	TypeSessionStart = "session.start"
	TypeBlob         = "blob"     // announces an upload content stream
	TypeSyncDone     = "sync.done"
	TypePrompt       = "prompt"
	TypeInterrupt    = "interrupt"
	TypeSessionEnd   = "session.end"

	// Server -> Client
	TypeSessionReady = "session.ready"
	TypeSyncRequest  = "sync.request"
	TypeStreamEvent  = "stream.event"
	TypeFileChange   = "file.change" // announces a file-change content stream
	TypeTurnDone     = "turn.done"
	TypeError        = "error"
)

// Envelope is the JSON structure inside every tagEnvelope record.
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ---- Payload DTOs -----------------------------------------------------------

// FileEntryDTO mirrors domain.FileEntry on the wire.
type FileEntryDTO struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

// SessionStartData is the payload of TypeSessionStart.
type SessionStartData struct {
	ProjectName string         `json:"projectName"`
	Manifest    []FileEntryDTO `json:"manifest"`
}

// SyncRequestData is the payload of TypeSyncRequest: hashes the server needs.
type SyncRequestData struct {
	Missing []string `json:"missing"`
}

// BlobData announces an upload content stream for one blob (client -> server).
type BlobData struct {
	StreamID uint64 `json:"streamId"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
}

// PromptData is the payload of TypePrompt.
type PromptData struct {
	Text string `json:"text"`
}

// StreamEventData wraps one agent event (server -> client).
type StreamEventData struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// FileChangeData announces a file-change content stream (server -> client). For
// deletes, StreamID is 0 and no chunks follow.
type FileChangeData struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	Mode     uint32 `json:"mode,omitempty"`
	Hash     string `json:"hash,omitempty"`
	StreamID uint64 `json:"streamId,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// TurnDoneData is the payload of TypeTurnDone.
type TurnDoneData struct {
	IsError  bool    `json:"isError"`
	CostUSD  float64 `json:"costUSD,omitempty"`
	Text     string  `json:"text,omitempty"`
	NumTurns int     `json:"numTurns,omitempty"`
}

// ErrorData is the payload of TypeError.
type ErrorData struct {
	Message string `json:"message"`
}

// SessionReadyData is the payload of TypeSessionReady.
type SessionReadyData struct {
	SessionID string `json:"sessionId"`
}

// UnmarshalData decodes an envelope's Data payload into v. A nil/empty Data is
// treated as an empty object so messages without a payload decode cleanly.
func UnmarshalData(env Envelope, v any) error {
	if len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, v)
}

// ---- Record framing ---------------------------------------------------------

// EncodeEnvelope marshals an envelope into a tagged record ready for Seal.
func EncodeEnvelope(env Envelope) ([]byte, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxRecordPlaintext {
		return nil, errors.New("protocol: envelope exceeds max record size")
	}
	out := make([]byte, 0, len(b)+1)
	out = append(out, tagEnvelope)
	out = append(out, b...)
	return out, nil
}

// EncodeChunk builds a tagged chunk record: tag | streamID(8) | seq(4) | final(1) | payload.
func EncodeChunk(streamID uint64, seq uint32, final bool, payload []byte) []byte {
	out := make([]byte, 1+8+4+1+len(payload))
	out[0] = tagChunk
	binary.BigEndian.PutUint64(out[1:9], streamID)
	binary.BigEndian.PutUint32(out[9:13], seq)
	if final {
		out[13] = 1
	}
	copy(out[14:], payload)
	return out
}

// Record is a decoded frame: exactly one of Envelope or Chunk is populated.
type Record struct {
	IsEnvelope bool
	Envelope   Envelope
	Chunk      Chunk
}

// Chunk is a decoded content-stream chunk.
type Chunk struct {
	StreamID uint64
	Seq      uint32
	Final    bool
	Payload  []byte
}

// DecodeRecord parses a decrypted record produced by EncodeEnvelope/EncodeChunk.
func DecodeRecord(b []byte) (Record, error) {
	if len(b) == 0 {
		return Record{}, errors.New("protocol: empty record")
	}
	switch b[0] {
	case tagEnvelope:
		var env Envelope
		if err := json.Unmarshal(b[1:], &env); err != nil {
			return Record{}, err
		}
		return Record{IsEnvelope: true, Envelope: env}, nil
	case tagChunk:
		if len(b) < 14 {
			return Record{}, errors.New("protocol: short chunk header")
		}
		c := Chunk{
			StreamID: binary.BigEndian.Uint64(b[1:9]),
			Seq:      binary.BigEndian.Uint32(b[9:13]),
			Final:    b[13] == 1,
			Payload:  append([]byte(nil), b[14:]...),
		}
		return Record{Chunk: c}, nil
	default:
		return Record{}, errors.New("protocol: unknown record tag")
	}
}
