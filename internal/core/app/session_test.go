package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
)

// ---- fakes ------------------------------------------------------------------

type fakeBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{m: map[string][]byte{}} }
func (f *fakeBlobs) Has(h string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[h]
	return ok, nil
}
func (f *fakeBlobs) Put(h string, c []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[h] = append([]byte(nil), c...)
	return nil
}
func (f *fakeBlobs) Get(h string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[h], nil
}

// fakeWorkspace lets the test control the snapshot before/after a turn.
type fakeWorkspace struct {
	mu    sync.Mutex
	files map[string]domain.FileEntry
	body  map[string][]byte
}

func newFakeWorkspace() *fakeWorkspace {
	return &fakeWorkspace{files: map[string]domain.FileEntry{}, body: map[string][]byte{}}
}
func (w *fakeWorkspace) Dir() string { return "/fake" }
func (w *fakeWorkspace) Materialize(m domain.Manifest, _ ports.BlobStore) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range m.Entries {
		w.files[e.Path] = e
	}
	return nil
}
func (w *fakeWorkspace) Snapshot() (domain.Manifest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var es []domain.FileEntry
	for _, e := range w.files {
		es = append(es, e)
	}
	return domain.Manifest{Entries: es}, nil
}
func (w *fakeWorkspace) ReadFile(p string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body[p], nil
}
func (w *fakeWorkspace) Remove() error { return nil }
func (w *fakeWorkspace) setFile(path, hash string, content []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[path] = domain.FileEntry{Path: path, Hash: hash}
	w.body[path] = content
}

// fakeAgent emits scripted events. onSend is invoked when a prompt arrives.
type fakeAgent struct {
	events chan domain.AgentEvent
	onSend func(prompt string)
}

func (a *fakeAgent) Start(_ context.Context, _, _ string) (ports.AgentSession, error) {
	a.events <- domain.AgentEvent{Kind: domain.AgentInit}
	return a, nil
}
func (a *fakeAgent) Send(_ context.Context, prompt string) error {
	a.onSend(prompt)
	return nil
}
func (a *fakeAgent) Events() <-chan domain.AgentEvent { return a.events }
func (a *fakeAgent) Interrupt() error                 { return nil }
func (a *fakeAgent) Close() error                     { close(a.events); return nil }

// fakeNotifier records outbound calls.
type fakeNotifier struct {
	mu       sync.Mutex
	stream   []domain.AgentEvent
	changes  []domain.FileChange
	turnDone []domain.TurnResult
}

func (n *fakeNotifier) SessionReady(string) error { return nil }
func (n *fakeNotifier) SyncRequest([]string) error { return nil }
func (n *fakeNotifier) StreamEvent(ev domain.AgentEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stream = append(n.stream, ev)
	return nil
}
func (n *fakeNotifier) FileChange(ch domain.FileChange, _ []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changes = append(n.changes, ch)
	return nil
}
func (n *fakeNotifier) TurnDone(res domain.TurnResult) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turnDone = append(n.turnDone, res)
	return nil
}
func (n *fakeNotifier) Error(string) error { return nil }

// ---- test -------------------------------------------------------------------

func TestHandlePromptOrchestration(t *testing.T) {
	blobs := newFakeBlobs()
	ws := newFakeWorkspace()
	notifier := &fakeNotifier{}
	agent := &fakeAgent{events: make(chan domain.AgentEvent, 16)}

	// On prompt: the "agent" creates HELLO.md, streams text, then a result.
	agent.onSend = func(prompt string) {
		ws.setFile("HELLO.md", "hash-hello", []byte("hi\n"))
		agent.events <- domain.AgentEvent{Kind: domain.AgentMessage, Text: "creating file"}
		agent.events <- domain.AgentEvent{Kind: domain.AgentResult, Result: &domain.TurnResult{Text: "done", CostUSD: 0.01}}
	}

	svc := NewService(Deps{
		Blobs:        blobs,
		NewWorkspace: func(string) (ports.Workspace, error) { return ws, nil },
		Agent:        agent,
		Notifier:     notifier,
		NewID:        func() string { return "11111111-1111-1111-1111-111111111111" },
	})

	ctx := context.Background()
	if _, err := svc.StartSession(ctx, "proj", domain.Manifest{}); err != nil {
		t.Fatal(err)
	}
	id, err := svc.SyncDone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected session id")
	}

	done := make(chan error, 1)
	go func() { done <- svc.HandlePrompt(ctx, "create HELLO.md") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandlePrompt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandlePrompt timed out")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.changes) != 1 || notifier.changes[0].Path != "HELLO.md" || notifier.changes[0].Op != domain.ChangeCreate {
		t.Fatalf("expected one create for HELLO.md, got %+v", notifier.changes)
	}
	if len(notifier.turnDone) != 1 || notifier.turnDone[0].Text != "done" {
		t.Fatalf("expected turn done with text, got %+v", notifier.turnDone)
	}
	// The init event plus the assistant message plus the result were streamed.
	if len(notifier.stream) < 3 {
		t.Fatalf("expected >=3 streamed events, got %d", len(notifier.stream))
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
