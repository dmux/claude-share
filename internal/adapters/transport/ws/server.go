package ws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/coder/websocket"

	"github.com/dmux/claude-share/internal/adapters/crypto/noise"
	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/protocol"
)

// ServiceFactory builds a per-connection SessionService bound to the given
// notifier. The composition root supplies this.
type ServiceFactory func(ports.ClientNotifier) ports.SessionService

// ServerConfig configures the WebSocket server.
type ServerConfig struct {
	Addr        string // e.g. "127.0.0.1:8443"
	PSK         []byte // 32-byte Noise pre-shared key derived from the token
	AllowPublic bool   // required to bind a non-loopback / wildcard address
	MaxSessions int    // max concurrent sessions (0 = unlimited)
	Logger      *log.Logger
}

// Server accepts client connections, performs the Noise handshake, and drives a
// SessionService per connection.
type Server struct {
	cfg     ServerConfig
	factory ServiceFactory
	slots   chan struct{} // session concurrency limiter (nil = unlimited)
}

// NewServer constructs a Server.
func NewServer(cfg ServerConfig, factory ServiceFactory) (*Server, error) {
	if len(cfg.PSK) != noise.PSKLen {
		return nil, errors.New("ws: server PSK must be 32 bytes")
	}
	if err := checkBind(cfg.Addr, cfg.AllowPublic); err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	s := &Server{cfg: cfg, factory: factory}
	if cfg.MaxSessions > 0 {
		s.slots = make(chan struct{}, cfg.MaxSessions)
	}
	return s, nil
}

// ListenAndServe blocks serving until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handle)

	hs := &http.Server{Addr: s.cfg.Addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = hs.Close()
	}()
	s.cfg.Logger.Printf("claude-share server listening on %s/ws", s.cfg.Addr)
	err := hs.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	wsc, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.cfg.Logger.Printf("accept: %v", err)
		return
	}
	ctx := r.Context()
	defer wsc.Close(websocket.StatusInternalError, "closing")

	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			s.cfg.Logger.Printf("rejecting connection: session limit (%d) reached", s.cfg.MaxSessions)
			wsc.Close(websocket.StatusTryAgainLater, "server at capacity")
			return
		}
	}

	// Noise handshake as responder over raw frames.
	send := func(b []byte) error { return wsc.Write(ctx, websocket.MessageBinary, b) }
	recv := func() ([]byte, error) {
		_, d, err := wsc.Read(ctx)
		return d, err
	}
	ch, err := noise.Handshake(noise.Config{PSK: s.cfg.PSK, Initiator: false}, send, recv)
	if err != nil {
		s.cfg.Logger.Printf("handshake rejected: %v", err)
		wsc.Close(websocket.StatusPolicyViolation, "handshake failed")
		return
	}

	c := newConn(wsc, ch)
	svc := s.factory(newNotifier(ctx, c))
	defer svc.Close()

	if err := s.dispatch(ctx, c, svc); err != nil && !isClosed(err) {
		s.cfg.Logger.Printf("session ended: %v", err)
	}
	wsc.Close(websocket.StatusNormalClosure, "bye")
}

func (s *Server) dispatch(ctx context.Context, c *conn, svc ports.SessionService) error {
	notify := newNotifier(ctx, c)
	for {
		msg, err := c.ReadMessage(ctx)
		if err != nil {
			return err
		}
		switch msg.Env.Type {
		case protocol.TypeSessionStart:
			var d protocol.SessionStartData
			if err := unmarshal(msg, &d); err != nil {
				return err
			}
			missing, err := svc.StartSession(ctx, d.ProjectName, toManifest(d.Manifest))
			if err != nil {
				_ = notify.Error(err.Error())
				return err
			}
			if err := notify.SyncRequest(missing); err != nil {
				return err
			}

		case protocol.TypeBlob:
			var d protocol.BlobData
			if err := unmarshal(msg, &d); err != nil {
				return err
			}
			if err := svc.PutBlob(ctx, d.Hash, msg.Content); err != nil {
				_ = notify.Error(err.Error())
				return err
			}

		case protocol.TypeSyncDone:
			id, err := svc.SyncDone(ctx)
			if err != nil {
				_ = notify.Error(err.Error())
				return err
			}
			if err := notify.SessionReady(id); err != nil {
				return err
			}

		case protocol.TypePrompt:
			var d protocol.PromptData
			if err := unmarshal(msg, &d); err != nil {
				return err
			}
			go func() {
				if err := svc.HandlePrompt(ctx, d.Text); err != nil {
					_ = notify.Error(err.Error())
				}
			}()

		case protocol.TypeInterrupt:
			if err := svc.Interrupt(ctx); err != nil {
				_ = notify.Error(err.Error())
			}

		case protocol.TypeSessionEnd:
			return nil

		default:
			s.cfg.Logger.Printf("unknown message type %q", msg.Env.Type)
		}
	}
}

func toManifest(dto []protocol.FileEntryDTO) domain.Manifest {
	entries := make([]domain.FileEntry, len(dto))
	for i, e := range dto {
		entries[i] = domain.FileEntry{Path: e.Path, Hash: e.Hash, Size: e.Size, Mode: domain.FileMode(e.Mode)}
	}
	return domain.Manifest{Entries: entries}
}

// checkBind refuses wildcard / non-loopback addresses unless allowPublic is set,
// enforcing the "internal network only" posture by default.
func checkBind(addr string, allowPublic bool) error {
	if allowPublic {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ws: invalid addr %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("ws: refusing to bind wildcard address %q without --allow-public (internal-network-only default)", addr)
	}
	ip := net.ParseIP(host)
	if ip != nil && !ip.IsLoopback() {
		return fmt.Errorf("ws: refusing to bind non-loopback address %q without --allow-public", addr)
	}
	return nil
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return true
	}
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}
