package passphrase

import (
	"strings"
	"testing"

	"github.com/dmux/claude-share/internal/adapters/crypto/noise"
)

func TestWordlistLoaded(t *testing.T) {
	if got := len(words); got != 7776 {
		t.Fatalf("wordlist size = %d, want 7776", got)
	}
	for i, w := range words {
		if w == "" || strings.TrimSpace(w) != w {
			t.Fatalf("word %d is blank or has surrounding whitespace: %q", i, w)
		}
	}
}

func TestGenerateShape(t *testing.T) {
	member := make(map[string]bool, len(words))
	for _, w := range words {
		member[w] = true
	}

	out, err := Generate(DefaultWords)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	parts := strings.Split(out, "-")
	if len(parts) != DefaultWords {
		t.Fatalf("got %d words (%q), want %d", len(parts), out, DefaultWords)
	}
	for _, p := range parts {
		if !member[p] {
			t.Fatalf("word %q is not in the wordlist", p)
		}
	}
}

func TestGenerateRejectsBadCount(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if _, err := Generate(n); err == nil {
			t.Fatalf("Generate(%d) = nil error, want error", n)
		}
	}
}

func TestGenerateRandomness(t *testing.T) {
	const iterations = 100
	seen := make(map[string]bool, iterations)
	for range iterations {
		out, err := Generate(DefaultWords)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[out] {
			t.Fatalf("duplicate passphrase generated: %q", out)
		}
		seen[out] = true
	}
}

// TestGenerateFeedsPipeline guards the end-to-end contract: a generated
// passphrase is a valid token for the Noise PSK derivation.
func TestGenerateFeedsPipeline(t *testing.T) {
	out, err := Generate(DefaultWords)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	psk, err := noise.DerivePSK(out)
	if err != nil {
		t.Fatalf("DerivePSK(%q): %v", out, err)
	}
	if len(psk) != noise.PSKLen {
		t.Fatalf("PSK length = %d, want %d", len(psk), noise.PSKLen)
	}
}
