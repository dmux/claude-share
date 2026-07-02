package claudecli

import (
	"testing"

	"github.com/dmux/claude-share/internal/core/domain"
)

// Representative stream-json lines captured from `claude` v2.1.198.
const (
	lineHook      = `{"type":"system","subtype":"hook_started","hook_id":"x","hook_name":"SessionStart:startup"}`
	lineInit      = `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"abc","model":"claude-opus-4-8","permissionMode":"acceptEdits"}`
	lineAssistant = `{"type":"assistant","message":{"model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"OK"}]},"session_id":"abc"}`
	lineToolUse   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Write","input":{}}]},"session_id":"abc"}`
	lineRateLimit = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`
	lineResult    = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"OK","total_cost_usd":0.074}`
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantOK  bool
		wantKind domain.AgentEventKind
	}{
		{"hook chatter dropped", lineHook, false, ""},
		{"init", lineInit, true, domain.AgentInit},
		{"assistant text", lineAssistant, true, domain.AgentMessage},
		{"tool use", lineToolUse, true, domain.AgentToolUse},
		{"rate limit dropped", lineRateLimit, false, ""},
		{"result", lineResult, true, domain.AgentResult},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok := parseLine([]byte(c.line))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if ev.Kind != c.wantKind {
				t.Fatalf("kind = %q, want %q", ev.Kind, c.wantKind)
			}
		})
	}
}

func TestParseAssistantText(t *testing.T) {
	ev, ok := parseLine([]byte(lineAssistant))
	if !ok || ev.Text != "OK" {
		t.Fatalf("expected text OK, got ok=%v text=%q", ok, ev.Text)
	}
}

func TestParseResultFields(t *testing.T) {
	ev, ok := parseLine([]byte(lineResult))
	if !ok || ev.Result == nil {
		t.Fatal("expected result event with populated Result")
	}
	if ev.Result.IsError {
		t.Error("IsError should be false")
	}
	if ev.Result.Text != "OK" {
		t.Errorf("Text = %q, want OK", ev.Result.Text)
	}
	if ev.Result.CostUSD != 0.074 {
		t.Errorf("CostUSD = %v, want 0.074", ev.Result.CostUSD)
	}
	if ev.Result.NumTurns != 1 {
		t.Errorf("NumTurns = %v, want 1", ev.Result.NumTurns)
	}
}
