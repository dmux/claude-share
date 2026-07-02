package ws

import (
	"context"
	"net/http"

	"github.com/coder/websocket"

	"github.com/dmux/claude-share/internal/adapters/crypto/noise"
	"github.com/dmux/claude-share/internal/protocol"
)

// Client is the client-side transport: an established, encrypted connection to
// the server with typed send helpers and a raw message reader.
type Client struct {
	conn *conn
}

// Dial connects to url (e.g. "ws://host:port/ws"), performs the Noise handshake
// as initiator using psk, and returns a ready Client.
func Dial(ctx context.Context, url string, psk []byte, header http.Header) (*Client, error) {
	wsc, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}

	send := func(b []byte) error { return wsc.Write(ctx, websocket.MessageBinary, b) }
	recv := func() ([]byte, error) {
		_, d, err := wsc.Read(ctx)
		return d, err
	}
	ch, err := noise.Handshake(noise.Config{PSK: psk, Initiator: true}, send, recv)
	if err != nil {
		wsc.Close(websocket.StatusPolicyViolation, "handshake failed")
		return nil, err
	}
	return &Client{conn: newConn(wsc, ch)}, nil
}

// SessionStart announces the project and its manifest.
func (c *Client) SessionStart(ctx context.Context, projectName string, manifest []protocol.FileEntryDTO) error {
	env, err := marshalData(protocol.TypeSessionStart, protocol.SessionStartData{
		ProjectName: projectName,
		Manifest:    manifest,
	})
	if err != nil {
		return err
	}
	return c.conn.SendEnvelope(ctx, env)
}

// UploadBlob sends one file's content as a content stream keyed by its hash.
func (c *Client) UploadBlob(ctx context.Context, hash string, content []byte) error {
	sid := c.conn.NewStreamID()
	env, err := marshalData(protocol.TypeBlob, protocol.BlobData{StreamID: sid, Hash: hash, Size: int64(len(content))})
	if err != nil {
		return err
	}
	if err := c.conn.SendEnvelope(ctx, env); err != nil {
		return err
	}
	return c.conn.SendContent(ctx, sid, content)
}

// SyncDone tells the server all blobs are uploaded.
func (c *Client) SyncDone(ctx context.Context) error {
	return c.conn.SendEnvelope(ctx, protocol.Envelope{Type: protocol.TypeSyncDone})
}

// Prompt sends a user prompt.
func (c *Client) Prompt(ctx context.Context, text string) error {
	env, err := marshalData(protocol.TypePrompt, protocol.PromptData{Text: text})
	if err != nil {
		return err
	}
	return c.conn.SendEnvelope(ctx, env)
}

// Interrupt requests a best-effort cancel of the current turn.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.conn.SendEnvelope(ctx, protocol.Envelope{Type: protocol.TypeInterrupt})
}

// SessionEnd asks the server to tear down the session.
func (c *Client) SessionEnd(ctx context.Context) error {
	return c.conn.SendEnvelope(ctx, protocol.Envelope{Type: protocol.TypeSessionEnd})
}

// ReadMessage returns the next assembled message from the server.
func (c *Client) ReadMessage(ctx context.Context) (Framed, error) {
	return c.conn.ReadMessage(ctx)
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "bye")
}

// PSKFromToken derives the Noise PSK from an operator token.
func PSKFromToken(token string) ([]byte, error) { return noise.DerivePSK(token) }

// unmarshal decodes an envelope's Data into v.
func unmarshal(msg Framed, v any) error {
	return protocol.UnmarshalData(msg.Env, v)
}
