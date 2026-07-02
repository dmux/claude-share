// Package domain holds the pure business types shared across the application.
//
// It has zero knowledge of transport, the Claude CLI, the filesystem, or
// cryptography. Everything the outside world provides is expressed through the
// interfaces in package ports; domain only describes the data those ports move.
package domain

import "time"

// FileMode is a Unix file mode stored as a portable uint32 so it can cross the
// wire without depending on os.FileMode's platform representation.
type FileMode uint32

// FileEntry describes a single file in a project snapshot. Content is never
// carried here; only its content-addressed hash. Bytes travel separately as
// blobs keyed by Hash.
type FileEntry struct {
	Path string   `json:"path"` // slash-separated, relative to the project root
	Hash string   `json:"hash"` // lowercase hex SHA-256 of the file content
	Size int64    `json:"size"`
	Mode FileMode `json:"mode"`
}

// Manifest is a snapshot of a project tree: the set of files and their hashes.
type Manifest struct {
	Entries []FileEntry `json:"entries"`
}

// ByPath returns the manifest indexed by path for O(1) lookups during diffing.
func (m Manifest) ByPath() map[string]FileEntry {
	idx := make(map[string]FileEntry, len(m.Entries))
	for _, e := range m.Entries {
		idx[e.Path] = e
	}
	return idx
}

// ChangeOp is the kind of change applied to a file between two manifests.
type ChangeOp string

const (
	ChangeCreate ChangeOp = "create"
	ChangeModify ChangeOp = "modify"
	ChangeDelete ChangeOp = "delete"
)

// FileChange is a single filesystem mutation produced by diffing the workspace
// before and after an agent turn. For create/modify, Hash points at the blob
// carrying the new content; for delete it is empty.
type FileChange struct {
	Op   ChangeOp `json:"op"`
	Path string   `json:"path"`
	Hash string   `json:"hash,omitempty"`
	Mode FileMode `json:"mode,omitempty"`
}

// AgentEventKind classifies a streamed event coming out of the agent.
type AgentEventKind string

const (
	// AgentInit is emitted once when the agent session is ready.
	AgentInit AgentEventKind = "init"
	// AgentMessage carries assistant output (possibly a partial delta).
	AgentMessage AgentEventKind = "message"
	// AgentToolUse / AgentToolResult mirror the agent's tool activity.
	AgentToolUse    AgentEventKind = "tool_use"
	AgentToolResult AgentEventKind = "tool_result"
	// AgentResult marks the end of a turn.
	AgentResult AgentEventKind = "result"
	// AgentUnknown is any event we pass through verbatim without interpreting.
	AgentUnknown AgentEventKind = "unknown"
)

// AgentEvent is a transport-neutral view of one Claude stream-json event. Raw
// holds the original JSON so the client can render fidelity we do not model.
type AgentEvent struct {
	Kind AgentEventKind `json:"kind"`
	// Text is the human-readable payload for AgentMessage events, if any.
	Text string `json:"text,omitempty"`
	// Raw is the untouched stream-json line for the client to render/inspect.
	Raw []byte `json:"raw,omitempty"`
	// Result fields, populated only for AgentResult.
	Result *TurnResult `json:"result,omitempty"`
}

// TurnResult summarizes a completed turn.
type TurnResult struct {
	IsError  bool    `json:"isError"`
	CostUSD  float64 `json:"costUSD,omitempty"`
	Text     string  `json:"text,omitempty"`
	NumTurns int     `json:"numTurns,omitempty"`
}

// SessionState is the lifecycle stage of a session.
type SessionState string

const (
	SessionSyncing SessionState = "syncing"
	SessionReady   SessionState = "ready"
	SessionClosed  SessionState = "closed"
)

// Session is the server-side record of one client's shared Claude session.
type Session struct {
	ID           string
	ProjectName  string
	WorkspaceDir string
	State        SessionState
	CreatedAt    time.Time
}
