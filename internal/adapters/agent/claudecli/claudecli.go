// Package claudecli implements the AgentRunner port by driving the local
// `claude` CLI in headless stream-json mode.
//
// One subprocess backs one session and stays alive for the whole multi-turn
// conversation: prompts are written to stdin as stream-json user messages, and
// stdout is parsed line-by-line into domain.AgentEvent values. The subprocess
// inherits the server's environment so the operator's existing Claude
// authentication (subscription or API key) is used.
package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
)

// ErrInterruptUnsupported is returned by Interrupt: headless stream-json mode
// has no clean mid-turn cancel. Callers may Close the session to stop it.
var ErrInterruptUnsupported = errors.New("claudecli: mid-turn interrupt not supported")

// Runner creates agent sessions using a configured `claude` binary.
type Runner struct {
	// Bin is the claude executable (default "claude").
	Bin string
	// PermissionMode passed via --permission-mode (default "acceptEdits").
	PermissionMode string
	// ExtraArgs are appended to every invocation (e.g. --model).
	ExtraArgs []string
}

// NewRunner returns a Runner with sensible defaults.
func NewRunner() *Runner {
	return &Runner{Bin: "claude", PermissionMode: "acceptEdits"}
}

// Start launches a claude subprocess rooted at workspaceDir.
func (r *Runner) Start(ctx context.Context, sessionID, workspaceDir string) (ports.AgentSession, error) {
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	perm := r.PermissionMode
	if perm == "" {
		perm = "acceptEdits"
	}
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", perm,
		"--session-id", sessionID,
	}
	args = append(args, r.ExtraArgs...)

	cmd := exec.Command(bin, args...)
	cmd.Dir = workspaceDir
	// Inherit env so existing Claude auth is used.

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &ringBuffer{max: 8 << 10}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &session{
		cmd:    cmd,
		stdin:  stdin,
		stderr: stderr,
		events: make(chan domain.AgentEvent, 64),
	}
	go s.readLoop(stdout)
	return s, nil
}

type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *ringBuffer
	events chan domain.AgentEvent

	writeMu   sync.Mutex
	closeOnce sync.Once
}

// userMessage is the stream-json envelope for a prompt.
type userMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string        `json:"role"`
		Content []textContent `json:"content"`
	} `json:"message"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Send writes one prompt to the subprocess stdin.
func (s *session) Send(ctx context.Context, prompt string) error {
	var m userMessage
	m.Type = "user"
	m.Message.Role = "user"
	m.Message.Content = []textContent{{Type: "text", Text: prompt}}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(b); err != nil {
		return fmt.Errorf("claudecli: write prompt: %w", err)
	}
	return nil
}

func (s *session) Events() <-chan domain.AgentEvent { return s.events }

func (s *session) Interrupt() error { return ErrInterruptUnsupported }

// Close closes stdin (signaling EOF so claude exits), then waits for the
// process, killing it if it does not exit promptly.
func (s *session) Close() error {
	s.closeOnce.Do(func() { _ = s.stdin.Close() })
	if err := s.cmd.Wait(); err != nil {
		if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
			return fmt.Errorf("claudecli: %w: %s", err, tail)
		}
		return err
	}
	return nil
}

// streamLine captures the fields we interpret from a stream-json line. Unknown
// fields are ignored; the full line is preserved as Raw for the client.
type streamLine struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	Message      json.RawMessage `json:"message"`
	IsError      bool            `json:"is_error"`
	Result       string          `json:"result"`
	NumTurns     int             `json:"num_turns"`
	TotalCostUSD float64         `json:"total_cost_usd"`
}

type assistantMessage struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// readLoop parses stdout into events until EOF, then closes the channel.
func (s *session) readLoop(stdout io.Reader) {
	defer close(s.events)
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if ev, ok := parseLine(line); ok {
				s.events <- ev
			}
		}
		if err != nil {
			return // EOF or read error ends the stream
		}
	}
}

// parseLine maps one stream-json line to an AgentEvent. It returns ok=false for
// lines that carry no useful signal (e.g. hook chatter) so they are dropped.
func parseLine(line []byte) (domain.AgentEvent, bool) {
	var sl streamLine
	if err := json.Unmarshal(line, &sl); err != nil {
		return domain.AgentEvent{}, false
	}
	trimmed := trimNewline(line)
	switch sl.Type {
	case "system":
		if sl.Subtype == "init" {
			return domain.AgentEvent{Kind: domain.AgentInit}, true
		}
		return domain.AgentEvent{}, false // drop hook_started/hook_response noise
	case "assistant":
		var am assistantMessage
		_ = json.Unmarshal(sl.Message, &am)
		var sb strings.Builder
		hasTool := false
		for _, c := range am.Content {
			if c.Type == "text" {
				sb.WriteString(c.Text)
			} else if c.Type == "tool_use" {
				hasTool = true
			}
		}
		if hasTool && sb.Len() == 0 {
			return domain.AgentEvent{Kind: domain.AgentToolUse, Raw: trimmed}, true
		}
		return domain.AgentEvent{Kind: domain.AgentMessage, Text: sb.String(), Raw: trimmed}, true
	case "user":
		return domain.AgentEvent{Kind: domain.AgentToolResult, Raw: trimmed}, true
	case "result":
		return domain.AgentEvent{
			Kind: domain.AgentResult,
			Raw:  trimmed,
			Result: &domain.TurnResult{
				IsError:  sl.IsError,
				CostUSD:  sl.TotalCostUSD,
				Text:     sl.Result,
				NumTurns: sl.NumTurns,
			},
		}, true
	default:
		return domain.AgentEvent{}, false // rate_limit_event, stream deltas, etc.
	}
}

func trimNewline(b []byte) []byte {
	out := append([]byte(nil), b...)
	for len(out) > 0 && (out[len(out)-1] == '\n' || out[len(out)-1] == '\r') {
		out = out[:len(out)-1]
	}
	return out
}

// ringBuffer keeps only the last max bytes written, for capturing a stderr tail.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
