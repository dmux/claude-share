package ws

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmux/claude-share/internal/adapters/crypto/noise"
	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/protocol"
)

// fakeSvc is a SessionService that scripts one turn with a large file change to
// exercise content-stream chunking end to end.
type fakeSvc struct {
	n       ports.ClientNotifier
	bigFile []byte
}

func (s *fakeSvc) StartSession(_ context.Context, _ string, _ domain.Manifest) ([]string, error) {
	return nil, nil
}
func (s *fakeSvc) PutBlob(_ context.Context, _ string, _ []byte) error { return nil }
func (s *fakeSvc) SyncDone(_ context.Context) (string, error)          { return "sess-1", nil }
func (s *fakeSvc) HandlePrompt(_ context.Context, _ string) error {
	if err := s.n.StreamEvent(domain.AgentEvent{Kind: domain.AgentMessage, Text: "working"}); err != nil {
		return err
	}
	if err := s.n.FileChange(domain.FileChange{Op: domain.ChangeCreate, Path: "big.txt", Hash: "h", Mode: 0o644}, s.bigFile); err != nil {
		return err
	}
	return s.n.TurnDone(domain.TurnResult{Text: "done", CostUSD: 0.01})
}
func (s *fakeSvc) Interrupt(_ context.Context) error { return nil }
func (s *fakeSvc) Close() error                      { return nil }

func TestTransportEndToEnd(t *testing.T) {
	token := "shared-secret"
	psk, err := noise.DerivePSK(token)
	if err != nil {
		t.Fatal(err)
	}

	// A file larger than one chunk to force multi-record reassembly.
	big := bytes.Repeat([]byte("A"), protocol.ChunkPayloadSize*2+123)

	srv, err := NewServer(ServerConfig{Addr: "127.0.0.1:0", PSK: psk}, func(n ports.ClientNotifier) ports.SessionService {
		return &fakeSvc{n: n, bigFile: big}
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Dial(ctx, wsURL, psk, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Sync handshake.
	if err := client.SessionStart(ctx, "proj", nil); err != nil {
		t.Fatal(err)
	}
	if msg, err := client.ReadMessage(ctx); err != nil || msg.Env.Type != protocol.TypeSyncRequest {
		t.Fatalf("expected sync.request, got %q err=%v", msg.Env.Type, err)
	}
	if err := client.SyncDone(ctx); err != nil {
		t.Fatal(err)
	}
	if msg, err := client.ReadMessage(ctx); err != nil || msg.Env.Type != protocol.TypeSessionReady {
		t.Fatalf("expected session.ready, got %q err=%v", msg.Env.Type, err)
	}

	// One turn.
	if err := client.Prompt(ctx, "make a big file"); err != nil {
		t.Fatal(err)
	}

	var gotStream, gotTurnDone bool
	var gotContent []byte
	for !gotTurnDone {
		msg, err := client.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		switch msg.Env.Type {
		case protocol.TypeStreamEvent:
			gotStream = true
		case protocol.TypeFileChange:
			if !msg.HasContent {
				t.Fatal("file change should carry content")
			}
			gotContent = msg.Content
		case protocol.TypeTurnDone:
			gotTurnDone = true
		}
	}
	if !gotStream {
		t.Error("expected a stream event")
	}
	if !bytes.Equal(gotContent, big) {
		t.Fatalf("reassembled content mismatch: got %d bytes want %d", len(gotContent), len(big))
	}
}

func TestTransportWrongTokenRejected(t *testing.T) {
	serverPSK, _ := noise.DerivePSK("server-token")
	clientPSK, _ := noise.DerivePSK("attacker-token")

	srv, err := NewServer(ServerConfig{Addr: "127.0.0.1:0", PSK: serverPSK}, func(n ports.ClientNotifier) ports.SessionService {
		return &fakeSvc{n: n}
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Dial(ctx, wsURL, clientPSK, nil); err == nil {
		t.Fatal("expected dial to fail with mismatched token")
	}
}

func TestCheckBind(t *testing.T) {
	cases := []struct {
		addr        string
		allowPublic bool
		ok          bool
	}{
		{"127.0.0.1:8443", false, true},
		{"localhost:8443", false, true}, // hostname passes; only parseable non-loopback IPs are blocked
		{"0.0.0.0:8443", false, false},
		{"0.0.0.0:8443", true, true},
		{"192.168.1.10:8443", false, false},
		{"192.168.1.10:8443", true, true},
	}
	for _, c := range cases {
		err := checkBind(c.addr, c.allowPublic)
		if c.ok && err != nil {
			t.Errorf("checkBind(%q,%v) unexpected error: %v", c.addr, c.allowPublic, err)
		}
		if !c.ok && err == nil {
			t.Errorf("checkBind(%q,%v) expected error", c.addr, c.allowPublic)
		}
	}
}
