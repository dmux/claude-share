// Package app contains the server-side application use cases: the concrete
// SessionService that orchestrates a shared Claude session for one client.
//
// It depends only on the ports; all I/O (agent, filesystem, transport) is
// injected. One Service instance backs one client connection.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/fsutil"
)

// Deps are the injected collaborators for a Service.
type Deps struct {
	// Blobs is the (server-wide) content-addressed store.
	Blobs ports.BlobStore
	// NewWorkspace builds an isolated workspace for a session id.
	NewWorkspace func(sessionID string) (ports.Workspace, error)
	// Agent starts Claude subprocesses.
	Agent ports.AgentRunner
	// Notifier pushes data back to the connected client.
	Notifier ports.ClientNotifier
	// NewID generates session identifiers (must be a valid UUID for claude).
	NewID func() string
}

// Service implements ports.SessionService for a single client connection.
type Service struct {
	deps Deps

	mu        sync.Mutex
	sessionID string
	manifest  domain.Manifest
	ws        ports.Workspace
	agent     ports.AgentSession

	preTurn  domain.Manifest
	turnDone chan error // signaled by the event pump when a turn completes
}

// NewService constructs a Service.
func NewService(deps Deps) *Service {
	return &Service{deps: deps, turnDone: make(chan error, 1)}
}

// StartSession registers the project manifest and returns the hashes the server
// is still missing and needs the client to upload.
func (s *Service) StartSession(ctx context.Context, projectName string, manifest domain.Manifest) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifest = manifest

	var missing []string
	seen := map[string]bool{}
	for _, e := range manifest.Entries {
		if seen[e.Hash] {
			continue
		}
		seen[e.Hash] = true
		has, err := s.deps.Blobs.Has(e.Hash)
		if err != nil {
			return nil, err
		}
		if !has {
			missing = append(missing, e.Hash)
		}
	}
	return missing, nil
}

// PutBlob stores one uploaded file's content.
func (s *Service) PutBlob(ctx context.Context, hash string, content []byte) error {
	return s.deps.Blobs.Put(hash, content)
}

// SyncDone materializes the workspace and starts the agent. It launches the
// event pump that forwards agent events and finalizes turns.
func (s *Service) SyncDone(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agent != nil {
		return s.sessionID, nil // already started
	}

	s.sessionID = s.deps.NewID()
	ws, err := s.deps.NewWorkspace(s.sessionID)
	if err != nil {
		return "", err
	}
	s.ws = ws
	if err := ws.Materialize(s.manifest, s.deps.Blobs); err != nil {
		return "", err
	}

	agent, err := s.deps.Agent.Start(ctx, s.sessionID, ws.Dir())
	if err != nil {
		return "", err
	}
	s.agent = agent

	go s.pump(agent.Events())
	return s.sessionID, nil
}

// pump forwards every agent event to the client and finalizes each turn when a
// result arrives. It runs until the agent's event channel closes.
func (s *Service) pump(events <-chan domain.AgentEvent) {
	for ev := range events {
		if err := s.deps.Notifier.StreamEvent(ev); err != nil {
			// Client gone; nothing more to do.
			return
		}
		if ev.Kind == domain.AgentResult {
			err := s.finishTurn(ev.Result)
			select {
			case s.turnDone <- err:
			default:
			}
		}
	}
}

// finishTurn diffs the workspace against the pre-turn snapshot and pushes the
// resulting file changes, then the turn summary.
func (s *Service) finishTurn(res *domain.TurnResult) error {
	after, err := s.ws.Snapshot()
	if err != nil {
		return err
	}
	changes := fsutil.Diff(s.preTurn, after)
	for _, ch := range changes {
		var content []byte
		if ch.Op != domain.ChangeDelete {
			content, err = s.ws.ReadFile(ch.Path)
			if err != nil {
				return err
			}
		}
		if err := s.deps.Notifier.FileChange(ch, content); err != nil {
			return err
		}
	}
	// Advance the baseline so the next turn diffs from here.
	s.preTurn = after

	tr := domain.TurnResult{}
	if res != nil {
		tr = *res
	}
	return s.deps.Notifier.TurnDone(tr)
}

// HandlePrompt snapshots the workspace, sends the prompt, and waits for the turn
// to complete (or the context to cancel).
func (s *Service) HandlePrompt(ctx context.Context, text string) error {
	s.mu.Lock()
	agent := s.agent
	ws := s.ws
	s.mu.Unlock()
	if agent == nil || ws == nil {
		return errors.New("app: session not ready")
	}

	snap, err := ws.Snapshot()
	if err != nil {
		return err
	}
	s.preTurn = snap

	// Drain any stale completion signal before starting.
	select {
	case <-s.turnDone:
	default:
	}

	if err := agent.Send(ctx, text); err != nil {
		return err
	}

	select {
	case err := <-s.turnDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Interrupt best-effort cancels the current turn.
func (s *Service) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return nil
	}
	return agent.Interrupt()
}

// Close tears down the agent and workspace.
func (s *Service) Close() error {
	s.mu.Lock()
	agent := s.agent
	ws := s.ws
	s.agent = nil
	s.ws = nil
	s.mu.Unlock()

	var errs []error
	if agent != nil {
		if err := agent.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if ws != nil {
		if err := ws.Remove(); err != nil {
			errs = append(errs, fmt.Errorf("remove workspace: %w", err))
		}
	}
	return errors.Join(errs...)
}
