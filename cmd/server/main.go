// Command claude-share-server shares this machine's authenticated Claude Code
// installation with clients on the internal network over an encrypted WebSocket
// channel. Each client gets an isolated workspace and its own claude subprocess.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"

	"github.com/dmux/claude-share/internal/adapters/agent/claudecli"
	"github.com/dmux/claude-share/internal/adapters/transport/ws"
	"github.com/dmux/claude-share/internal/adapters/workspace/fsstore"
	"github.com/dmux/claude-share/internal/core/app"
	"github.com/dmux/claude-share/internal/core/ports"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[claude-share] ")

	addr := flag.String("addr", "127.0.0.1:8443", "listen address (host:port)")
	dataDir := flag.String("data-dir", defaultDataDir(), "directory for the blob store and session workspaces")
	allowPublic := flag.Bool("allow-public", false, "allow binding a non-loopback/wildcard address (internal network only)")
	claudeBin := flag.String("claude-bin", "claude", "path to the claude executable")
	permMode := flag.String("permission-mode", "acceptEdits", "claude --permission-mode")
	maxSessions := flag.Int("max-sessions", 8, "max concurrent sessions (0 = unlimited)")
	flag.Parse()

	token := os.Getenv("CLAUDE_SHARE_TOKEN")
	if token == "" {
		log.Fatal("CLAUDE_SHARE_TOKEN must be set (shared secret for the encrypted channel)")
	}
	psk, err := ws.PSKFromToken(token)
	if err != nil {
		log.Fatalf("derive PSK: %v", err)
	}

	blobs, err := fsstore.NewBlobStore(filepath.Join(*dataDir, "objects"))
	if err != nil {
		log.Fatalf("blob store: %v", err)
	}

	runner := claudecli.NewRunner()
	runner.Bin = *claudeBin
	runner.PermissionMode = *permMode

	factory := func(n ports.ClientNotifier) ports.SessionService {
		return app.NewService(app.Deps{
			Blobs: blobs,
			NewWorkspace: func(sessionID string) (ports.Workspace, error) {
				dir := filepath.Join(*dataDir, "sessions", sessionID, "workspace")
				return fsstore.NewWorkspace(dir)
			},
			Agent:    runner,
			Notifier: n,
			NewID:    func() string { return uuid.NewString() },
		})
	}

	srv, err := ws.NewServer(ws.ServerConfig{
		Addr:        *addr,
		PSK:         psk,
		AllowPublic: *allowPublic,
		MaxSessions: *maxSessions,
	}, factory)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Println("shut down")
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-share"
	}
	return filepath.Join(home, ".claude-share")
}
