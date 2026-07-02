// Package ws is the WebSocket transport: the primary adapter on the server and
// the connection adapter on the client. Every application message is carried
// inside a Noise-sealed binary frame; large payloads are split into a content
// stream of chunk records.
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/protocol"
)

// readLimit caps a single WebSocket frame. A sealed record is at most 65535+16
// bytes; 1 MiB leaves generous headroom.
const readLimit = 1 << 20

// conn couples a WebSocket connection with an established secure channel and
// handles record framing and content-stream (re)assembly. It is safe for one
// concurrent writer and one concurrent reader.
type conn struct {
	ws *websocket.Conn
	ch ports.SecureChannel

	writeMu sync.Mutex
	outSeq  atomic.Uint64

	pending map[uint64]*asm // in-flight inbound content streams
}

func newConn(wsc *websocket.Conn, ch ports.SecureChannel) *conn {
	wsc.SetReadLimit(readLimit)
	return &conn{ws: wsc, ch: ch, pending: map[uint64]*asm{}}
}

type asm struct {
	env protocol.Envelope
	buf []byte
}

// Framed is a fully assembled inbound message.
type Framed struct {
	Env        protocol.Envelope
	Content    []byte
	HasContent bool
}

// NewStreamID allocates a unique outbound content-stream id (always >0).
func (c *conn) NewStreamID() uint64 { return c.outSeq.Add(1) }

// writeRecord seals and writes one plaintext record as a binary frame. It
// serializes with the write mutex so AEAD nonces stay in wire order.
func (c *conn) writeRecord(ctx context.Context, rec []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	sealed, err := c.ch.Seal(rec)
	if err != nil {
		return err
	}
	return c.ws.Write(ctx, websocket.MessageBinary, sealed)
}

// SendEnvelope sends a control/data envelope (no content stream).
func (c *conn) SendEnvelope(ctx context.Context, env protocol.Envelope) error {
	rec, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return err
	}
	return c.writeRecord(ctx, rec)
}

// SendContent sends content as a run of chunk records under streamID. A final
// (possibly empty) chunk always terminates the stream.
func (c *conn) SendContent(ctx context.Context, streamID uint64, content []byte) error {
	var seq uint32
	for {
		end := len(content)
		if end > protocol.ChunkPayloadSize {
			end = protocol.ChunkPayloadSize
		}
		final := end == len(content)
		rec := protocol.EncodeChunk(streamID, seq, final, content[:end])
		if err := c.writeRecord(ctx, rec); err != nil {
			return err
		}
		content = content[end:]
		seq++
		if final {
			return nil
		}
	}
}

// ReadMessage reads records until a full message is assembled: either a
// content-free envelope or an envelope plus its complete content stream.
func (c *conn) ReadMessage(ctx context.Context) (Framed, error) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return Framed{}, err
		}
		plain, err := c.ch.Open(data)
		if err != nil {
			return Framed{}, fmt.Errorf("ws: decrypt: %w", err)
		}
		rec, err := protocol.DecodeRecord(plain)
		if err != nil {
			return Framed{}, err
		}
		if rec.IsEnvelope {
			sid, err := streamIDOf(rec.Envelope)
			if err != nil {
				return Framed{}, err
			}
			if sid == 0 {
				return Framed{Env: rec.Envelope}, nil
			}
			c.pending[sid] = &asm{env: rec.Envelope}
			continue
		}
		a := c.pending[rec.Chunk.StreamID]
		if a == nil {
			return Framed{}, fmt.Errorf("ws: chunk for unknown stream %d", rec.Chunk.StreamID)
		}
		a.buf = append(a.buf, rec.Chunk.Payload...)
		if rec.Chunk.Final {
			delete(c.pending, rec.Chunk.StreamID)
			return Framed{Env: a.env, Content: a.buf, HasContent: true}, nil
		}
	}
}

// Close closes the underlying WebSocket.
func (c *conn) Close(code websocket.StatusCode, reason string) error {
	return c.ws.Close(code, reason)
}

// streamIDOf returns the content-stream id an envelope announces, or 0 if it
// carries no content.
func streamIDOf(env protocol.Envelope) (uint64, error) {
	switch env.Type {
	case protocol.TypeBlob:
		var d protocol.BlobData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return 0, err
		}
		return d.StreamID, nil
	case protocol.TypeFileChange:
		var d protocol.FileChangeData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return 0, err
		}
		return d.StreamID, nil
	default:
		return 0, nil
	}
}

// marshalData is a small helper to build an envelope with a JSON payload.
func marshalData(typ string, payload any) (protocol.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return protocol.Envelope{}, err
	}
	return protocol.Envelope{Type: typ, Data: b}, nil
}
