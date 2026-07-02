// Package terminal renders client-side activity to a console. It implements
// clientapp.Renderer.
package terminal

import (
	"fmt"
	"io"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/protocol"
)

// Renderer writes human-readable output. Info/status goes to Err; assistant
// output goes to Out.
type Renderer struct {
	Out io.Writer
	Err io.Writer
}

// New returns a Renderer writing to the given streams.
func New(out, err io.Writer) *Renderer { return &Renderer{Out: out, Err: err} }

func (r *Renderer) Info(format string, args ...any) {
	fmt.Fprintf(r.Err, "• "+format+"\n", args...)
}

func (r *Renderer) StreamEvent(d *protocol.StreamEventData) {
	switch domain.AgentEventKind(d.Kind) {
	case domain.AgentMessage:
		if d.Text != "" {
			fmt.Fprint(r.Out, d.Text)
		}
	case domain.AgentToolUse:
		fmt.Fprintf(r.Err, "\n  ⚙ tool use\n")
	case domain.AgentToolResult:
		fmt.Fprintf(r.Err, "  ⚙ tool result\n")
	}
}

func (r *Renderer) FileChanged(op, path string) {
	sym := map[string]string{"create": "+", "modify": "~", "delete": "-"}[op]
	if sym == "" {
		sym = "?"
	}
	fmt.Fprintf(r.Err, "\n  %s %s\n", sym, path)
}

func (r *Renderer) TurnDone(d *protocol.TurnDoneData) {
	fmt.Fprintln(r.Out)
	if d.IsError {
		fmt.Fprintf(r.Err, "• turn ended with error\n")
	}
	if d.CostUSD > 0 {
		fmt.Fprintf(r.Err, "• turn complete ($%.4f)\n", d.CostUSD)
	} else {
		fmt.Fprintf(r.Err, "• turn complete\n")
	}
}

func (r *Renderer) ServerError(msg string) {
	fmt.Fprintf(r.Err, "! server error: %s\n", msg)
}
