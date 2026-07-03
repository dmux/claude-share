// Command claude-share-server shares this machine's authenticated Claude Code
// installation with clients on the internal network over an encrypted WebSocket
// channel. Each client gets an isolated workspace and its own claude subprocess.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/dmux/claude-share/internal/adapters/agent/claudecli"
	"github.com/dmux/claude-share/internal/adapters/crypto/passphrase"
	"github.com/dmux/claude-share/internal/adapters/transport/ws"
	"github.com/dmux/claude-share/internal/adapters/workspace/fsstore"
	"github.com/dmux/claude-share/internal/core/app"
	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/version"
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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("claude-share-server %s\n", version.Version)
		return
	}

	token := os.Getenv("CLAUDE_SHARE_TOKEN")
	generated := false
	if token == "" {
		t, err := passphrase.Generate(passphrase.DefaultWords)
		if err != nil {
			log.Fatalf("generate share token: %v", err)
		}
		token, generated = t, true
	}
	psk, err := ws.PSKFromToken(token)
	if err != nil {
		log.Fatalf("derive PSK: %v", err)
	}
	if generated {
		printTokenBanner(os.Stderr, token)
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

// ANSI styles used by the token banner. Emitted only when the output is a TTY
// and NO_COLOR is unset.
const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiToken = "\033[1;36m"     // bold cyan — the passphrase itself
	ansiFrame = "\033[38;5;244m" // gray — the box border
)

// printTokenBanner renders the generated share token inside a framed, centered
// box so it stands out in the console instead of scrolling past as log lines.
// Color is applied only when f is a terminal (and NO_COLOR is unset).
func printTokenBanner(f *os.File, token string) {
	fmt.Fprint(f, renderTokenBanner(token, useColor(f)))
}

// renderTokenBanner builds the framed banner as a string. When color is false
// the output is plain ASCII/box-drawing text with no escape sequences.
func renderTokenBanner(token string, color bool) string {
	paint := func(s, code string) string {
		if !color || code == "" || s == "" {
			return s
		}
		return code + s + ansiReset
	}

	type line struct {
		text   string
		code   string
		center bool
	}
	lines := []line{
		{text: "claude-share · generated share token", code: ansiBold},
		{},
		{text: token, code: ansiToken, center: true},
		{},
		{text: "No CLAUDE_SHARE_TOKEN was set, so one was generated for this run.", code: ansiDim},
		{text: "Set CLAUDE_SHARE_TOKEN to the value above on every client to connect.", code: ansiDim},
	}

	width := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(l.text); n > width {
			width = n
		}
	}
	const padX = 3
	inner := width + padX*2

	var b strings.Builder
	b.WriteByte('\n')
	b.WriteString(paint("╭"+strings.Repeat("─", inner)+"╮", ansiFrame))
	b.WriteByte('\n')
	for _, l := range lines {
		vis := utf8.RuneCountInString(l.text)
		left := padX
		if l.center {
			left = padX + (inner-2*padX-vis)/2
		}
		right := inner - left - vis
		b.WriteString(paint("│", ansiFrame))
		b.WriteString(strings.Repeat(" ", left))
		b.WriteString(paint(l.text, l.code))
		b.WriteString(strings.Repeat(" ", right))
		b.WriteString(paint("│", ansiFrame))
		b.WriteByte('\n')
	}
	b.WriteString(paint("╰"+strings.Repeat("─", inner)+"╯", ansiFrame))
	b.WriteString("\n\n")
	return b.String()
}

// useColor reports whether ANSI styling should be applied to f: true only when
// f is a terminal and the NO_COLOR convention is not requesting plain output.
func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-share"
	}
	return filepath.Join(home, ".claude-share")
}
