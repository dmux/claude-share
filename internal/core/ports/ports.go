// Package ports declares the interfaces at the boundary of the hexagon.
//
// Driving ports are called by primary adapters (e.g. the WebSocket transport)
// to drive the application. Driven ports are implemented by secondary adapters
// (Claude CLI, filesystem, crypto) and consumed by the application. The core
// depends only on these interfaces, never on concrete adapters.
package ports

import (
	"context"

	"github.com/dmux/claude-share/internal/core/domain"
)

// ---- Driving (primary) port -------------------------------------------------

// SessionService is the application entry point a transport adapter drives for
// a single connected client. Implementations are expected to be used by one
// client connection at a time.
type SessionService interface {
	// StartSession registers a project snapshot and returns the hashes the
	// server is still missing (and therefore needs uploaded as blobs).
	StartSession(ctx context.Context, projectName string, manifest domain.Manifest) (missing []string, err error)
	// PutBlob stores one file's content, addressed by its hash.
	PutBlob(ctx context.Context, hash string, content []byte) error
	// SyncDone signals all missing blobs are uploaded; the workspace is
	// materialized and the agent is started. Returns the session id.
	SyncDone(ctx context.Context) (sessionID string, err error)
	// HandlePrompt runs one turn: it forwards the prompt to the agent, streams
	// events back through the ClientNotifier, then diffs the workspace and
	// pushes file changes. It returns when the turn completes.
	HandlePrompt(ctx context.Context, text string) error
	// Interrupt best-effort cancels the in-flight turn.
	Interrupt(ctx context.Context) error
	// Close tears down the agent and releases the workspace.
	Close() error
}

// ---- Driven (secondary) ports ----------------------------------------------

// ClientNotifier is the outbound port the application uses to push data to the
// connected client. Implemented by the transport adapter.
type ClientNotifier interface {
	SessionReady(sessionID string) error
	SyncRequest(missing []string) error
	StreamEvent(ev domain.AgentEvent) error
	FileChange(ch domain.FileChange, content []byte) error
	TurnDone(res domain.TurnResult) error
	Error(message string) error
}

// AgentRunner starts agent sessions. One AgentSession maps to one long-lived
// `claude` subprocess used for a whole multi-turn conversation.
type AgentRunner interface {
	Start(ctx context.Context, sessionID, workspaceDir string) (AgentSession, error)
}

// AgentSession is a running agent conversation.
type AgentSession interface {
	// Send submits a user prompt for the next turn.
	Send(ctx context.Context, prompt string) error
	// Events streams parsed events until the session closes.
	Events() <-chan domain.AgentEvent
	// Interrupt best-effort stops the current turn.
	Interrupt() error
	// Close terminates the subprocess.
	Close() error
}

// BlobStore is a content-addressed store of file bytes keyed by SHA-256 hash.
type BlobStore interface {
	Has(hash string) (bool, error)
	Put(hash string, content []byte) error
	Get(hash string) ([]byte, error)
}

// Workspace materializes and inspects a session's project tree on disk.
type Workspace interface {
	// Dir is the absolute path Claude runs in.
	Dir() string
	// Materialize writes the given manifest into the workspace, pulling
	// content from the BlobStore.
	Materialize(manifest domain.Manifest, blobs BlobStore) error
	// Snapshot computes the current manifest of the workspace.
	Snapshot() (domain.Manifest, error)
	// ReadFile returns the content of a workspace-relative path (path-safe).
	ReadFile(path string) ([]byte, error)
	// Remove deletes the workspace tree.
	Remove() error
}

// SecureChannel is an established, mutually-authenticated encrypted channel.
// Seal/Open operate on whole logical messages.
type SecureChannel interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(ciphertext []byte) ([]byte, error)
}

// Authenticator validates a client-presented credential. With a PSK handshake
// authentication is implicit, but the port keeps the door open for other
// schemes.
type Authenticator interface {
	Authenticate(token string) error
}
