// Command claude-share-client connects to a claude-share server over an
// encrypted WebSocket channel and drives a shared Claude Code session against
// the local project directory.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmux/claude-share/internal/adapters/localfs"
	"github.com/dmux/claude-share/internal/adapters/terminal"
	"github.com/dmux/claude-share/internal/adapters/transport/ws"
	"github.com/dmux/claude-share/internal/core/clientapp"
	"github.com/dmux/claude-share/internal/protocol"
)

func main() {
	server := flag.String("server", "ws://127.0.0.1:8443/ws", "server WebSocket URL")
	dir := flag.String("dir", ".", "local project directory to share")
	name := flag.String("name", "", "project name (defaults to the directory name)")
	maxFile := flag.Int64("max-file-size", 25<<20, "reject files larger than this many bytes (0 = unlimited)")
	maxTotal := flag.Int64("max-total-size", 500<<20, "reject a project larger than this many bytes (0 = unlimited)")
	flag.Parse()

	token := os.Getenv("CLAUDE_SHARE_TOKEN")
	if token == "" {
		log.Fatal("CLAUDE_SHARE_TOKEN must be set (must match the server's token)")
	}
	psk, err := ws.PSKFromToken(token)
	if err != nil {
		log.Fatalf("derive PSK: %v", err)
	}

	proj, err := localfs.Open(*dir, *maxFile, *maxTotal)
	if err != nil {
		log.Fatalf("open project: %v", err)
	}
	projectName := *name
	if projectName == "" {
		projectName = filepathBase(proj.Root())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := ws.Dial(ctx, *server, psk, nil)
	if err != nil {
		log.Fatalf("connect: %v (check the server is running and the token matches)", err)
	}
	defer client.Close()

	app := &clientapp.App{
		Project:     proj,
		Transport:   &wsTransport{c: client},
		Renderer:    terminal.New(os.Stdout, os.Stderr),
		ProjectName: projectName,
		Input:       os.Stdin,
	}
	if err := app.Run(ctx); err != nil {
		log.Fatalf("session: %v", err)
	}
}

// wsTransport adapts *ws.Client to clientapp.Transport, decoding inbound frames
// into typed ServerMessages.
type wsTransport struct{ c *ws.Client }

func (t *wsTransport) SessionStart(ctx context.Context, projectName string, manifest []protocol.FileEntryDTO) error {
	return t.c.SessionStart(ctx, projectName, manifest)
}
func (t *wsTransport) UploadBlob(ctx context.Context, hash string, content []byte) error {
	return t.c.UploadBlob(ctx, hash, content)
}
func (t *wsTransport) SyncDone(ctx context.Context) error    { return t.c.SyncDone(ctx) }
func (t *wsTransport) Prompt(ctx context.Context, s string) error { return t.c.Prompt(ctx, s) }
func (t *wsTransport) SessionEnd(ctx context.Context) error  { return t.c.SessionEnd(ctx) }

func (t *wsTransport) ReadServerMessage(ctx context.Context) (clientapp.ServerMessage, error) {
	f, err := t.c.ReadMessage(ctx)
	if err != nil {
		return clientapp.ServerMessage{}, err
	}
	m := clientapp.ServerMessage{Type: f.Env.Type, Content: f.Content}
	switch f.Env.Type {
	case protocol.TypeSessionReady:
		var d protocol.SessionReadyData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.SessionReady = &d
	case protocol.TypeSyncRequest:
		var d protocol.SyncRequestData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.SyncRequest = &d
	case protocol.TypeStreamEvent:
		var d protocol.StreamEventData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.StreamEvent = &d
	case protocol.TypeFileChange:
		var d protocol.FileChangeData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.FileChange = &d
	case protocol.TypeTurnDone:
		var d protocol.TurnDoneData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.TurnDone = &d
	case protocol.TypeError:
		var d protocol.ErrorData
		if err := protocol.UnmarshalData(f.Env, &d); err != nil {
			return m, err
		}
		m.Error = &d
	}
	return m, nil
}

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
