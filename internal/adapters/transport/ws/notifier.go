package ws

import (
	"context"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/protocol"
)

// notifier implements ports.ClientNotifier over a conn. All sends use the
// connection's lifetime context.
type notifier struct {
	ctx  context.Context
	conn *conn
}

func newNotifier(ctx context.Context, c *conn) *notifier {
	return &notifier{ctx: ctx, conn: c}
}

func (n *notifier) SessionReady(sessionID string) error {
	env, err := marshalData(protocol.TypeSessionReady, protocol.SessionReadyData{SessionID: sessionID})
	if err != nil {
		return err
	}
	return n.conn.SendEnvelope(n.ctx, env)
}

func (n *notifier) SyncRequest(missing []string) error {
	env, err := marshalData(protocol.TypeSyncRequest, protocol.SyncRequestData{Missing: missing})
	if err != nil {
		return err
	}
	return n.conn.SendEnvelope(n.ctx, env)
}

func (n *notifier) StreamEvent(ev domain.AgentEvent) error {
	env, err := marshalData(protocol.TypeStreamEvent, protocol.StreamEventData{
		Kind: string(ev.Kind),
		Text: ev.Text,
		Raw:  ev.Raw,
	})
	if err != nil {
		return err
	}
	return n.conn.SendEnvelope(n.ctx, env)
}

func (n *notifier) FileChange(ch domain.FileChange, content []byte) error {
	data := protocol.FileChangeData{
		Op:   string(ch.Op),
		Path: ch.Path,
		Mode: uint32(ch.Mode),
		Hash: ch.Hash,
	}
	if ch.Op != domain.ChangeDelete {
		data.StreamID = n.conn.NewStreamID()
		data.Size = int64(len(content))
	}
	env, err := marshalData(protocol.TypeFileChange, data)
	if err != nil {
		return err
	}
	if err := n.conn.SendEnvelope(n.ctx, env); err != nil {
		return err
	}
	if data.StreamID != 0 {
		return n.conn.SendContent(n.ctx, data.StreamID, content)
	}
	return nil
}

func (n *notifier) TurnDone(res domain.TurnResult) error {
	env, err := marshalData(protocol.TypeTurnDone, protocol.TurnDoneData{
		IsError:  res.IsError,
		CostUSD:  res.CostUSD,
		Text:     res.Text,
		NumTurns: res.NumTurns,
	})
	if err != nil {
		return err
	}
	return n.conn.SendEnvelope(n.ctx, env)
}

func (n *notifier) Error(message string) error {
	env, err := marshalData(protocol.TypeError, protocol.ErrorData{Message: message})
	if err != nil {
		return err
	}
	return n.conn.SendEnvelope(n.ctx, env)
}
