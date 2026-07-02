// Package clientapp holds the client-side use cases: the sync handshake and the
// interactive REPL loop. It depends only on interfaces (Transport, Renderer,
// Project) so the composition root can wire concrete adapters.
package clientapp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/protocol"
)

// ServerMessage is a decoded message from the server. Exactly one payload
// pointer is set according to Type.
type ServerMessage struct {
	Type         string
	SessionReady *protocol.SessionReadyData
	SyncRequest  *protocol.SyncRequestData
	StreamEvent  *protocol.StreamEventData
	FileChange   *protocol.FileChangeData
	Content      []byte
	TurnDone     *protocol.TurnDoneData
	Error        *protocol.ErrorData
}

// Transport is the client's view of the encrypted connection.
type Transport interface {
	SessionStart(ctx context.Context, projectName string, manifest []protocol.FileEntryDTO) error
	UploadBlob(ctx context.Context, hash string, content []byte) error
	SyncDone(ctx context.Context) error
	Prompt(ctx context.Context, text string) error
	SessionEnd(ctx context.Context) error
	ReadServerMessage(ctx context.Context) (ServerMessage, error)
}

// Project is the local project directory.
type Project interface {
	Root() string
	Manifest() (domain.Manifest, error)
	ReadFile(rel string) ([]byte, error)
	Apply(ch domain.FileChange, content []byte) error
}

// Renderer displays activity to the user.
type Renderer interface {
	Info(format string, args ...any)
	StreamEvent(d *protocol.StreamEventData)
	FileChanged(op, path string)
	TurnDone(d *protocol.TurnDoneData)
	ServerError(msg string)
}

// App drives one client session.
type App struct {
	Project     Project
	Transport   Transport
	Renderer    Renderer
	ProjectName string
	Input       io.Reader // where prompts are read from (usually os.Stdin)
}

// Run performs the sync handshake then runs the REPL until input ends or the
// context is cancelled.
func (a *App) Run(ctx context.Context) error {
	manifest, err := a.Project.Manifest()
	if err != nil {
		return fmt.Errorf("build manifest: %w", err)
	}
	dto, byHash := toDTO(manifest)

	// Reader goroutine dispatches all inbound messages via channels.
	syncReq := make(chan *protocol.SyncRequestData, 1)
	ready := make(chan string, 1)
	turnDone := make(chan struct{}, 1)
	readErr := make(chan error, 1)

	go a.readLoop(ctx, syncReq, ready, turnDone, readErr)

	a.Renderer.Info("Uploading project (%d files)...", len(dto))
	if err := a.Transport.SessionStart(ctx, a.ProjectName, dto); err != nil {
		return err
	}

	// Await the server's sync request, then upload the missing blobs.
	select {
	case req := <-syncReq:
		for _, hash := range req.Missing {
			path, ok := byHash[hash]
			if !ok {
				return fmt.Errorf("server requested unknown hash %s", hash)
			}
			content, err := a.Project.ReadFile(path)
			if err != nil {
				return err
			}
			if err := a.Transport.UploadBlob(ctx, hash, content); err != nil {
				return err
			}
		}
		a.Renderer.Info("Uploaded %d missing file(s).", len(req.Missing))
	case err := <-readErr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := a.Transport.SyncDone(ctx); err != nil {
		return err
	}

	select {
	case id := <-ready:
		a.Renderer.Info("Session ready: %s", id)
	case err := <-readErr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}

	return a.repl(ctx, turnDone, readErr)
}

// repl reads prompts from Input and dispatches them one turn at a time.
func (a *App) repl(ctx context.Context, turnDone chan struct{}, readErr chan error) error {
	sc := bufio.NewScanner(a.Input)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	a.Renderer.Info("Type a prompt and press Enter (Ctrl-D to quit).")
	for {
		fmt.Print("\n> ")
		if !sc.Scan() {
			return a.Transport.SessionEnd(ctx)
		}
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if err := a.Transport.Prompt(ctx, text); err != nil {
			return err
		}
		select {
		case <-turnDone:
		case err := <-readErr:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readLoop reads server messages, applies file changes, renders output, and
// signals milestones over the provided channels.
func (a *App) readLoop(ctx context.Context, syncReq chan<- *protocol.SyncRequestData, ready chan<- string, turnDone chan<- struct{}, readErr chan<- error) {
	for {
		msg, err := a.Transport.ReadServerMessage(ctx)
		if err != nil {
			select {
			case readErr <- err:
			default:
			}
			return
		}
		switch msg.Type {
		case protocol.TypeSyncRequest:
			syncReq <- msg.SyncRequest
		case protocol.TypeSessionReady:
			ready <- msg.SessionReady.SessionID
		case protocol.TypeStreamEvent:
			a.Renderer.StreamEvent(msg.StreamEvent)
		case protocol.TypeFileChange:
			a.applyChange(msg)
		case protocol.TypeTurnDone:
			a.Renderer.TurnDone(msg.TurnDone)
			select {
			case turnDone <- struct{}{}:
			default:
			}
		case protocol.TypeError:
			a.Renderer.ServerError(msg.Error.Message)
		}
	}
}

func (a *App) applyChange(msg ServerMessage) {
	fc := msg.FileChange
	ch := domain.FileChange{
		Op:   domain.ChangeOp(fc.Op),
		Path: fc.Path,
		Hash: fc.Hash,
		Mode: domain.FileMode(fc.Mode),
	}
	if err := a.Project.Apply(ch, msg.Content); err != nil {
		a.Renderer.ServerError(fmt.Sprintf("apply %s: %v", fc.Path, err))
		return
	}
	a.Renderer.FileChanged(fc.Op, fc.Path)
}

// toDTO converts a manifest to wire DTOs and a hash->path index for uploads.
func toDTO(m domain.Manifest) ([]protocol.FileEntryDTO, map[string]string) {
	dto := make([]protocol.FileEntryDTO, len(m.Entries))
	byHash := make(map[string]string, len(m.Entries))
	for i, e := range m.Entries {
		dto[i] = protocol.FileEntryDTO{Path: e.Path, Hash: e.Hash, Size: e.Size, Mode: uint32(e.Mode)}
		if _, ok := byHash[e.Hash]; !ok {
			byHash[e.Hash] = e.Path
		}
	}
	return dto, byHash
}
