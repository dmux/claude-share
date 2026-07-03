package main

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var ansiRE = regexp.MustCompile("\033\\[[0-9;]*m")

// bannerRows returns the framed lines (those beginning with a box-drawing char),
// stripped of ANSI escapes.
func bannerRows(banner string) []string {
	var rows []string
	for raw := range strings.SplitSeq(banner, "\n") {
		l := ansiRE.ReplaceAllString(raw, "")
		if strings.HasPrefix(l, "╭") || strings.HasPrefix(l, "│") || strings.HasPrefix(l, "╰") {
			rows = append(rows, l)
		}
	}
	return rows
}

func TestRenderTokenBannerAligned(t *testing.T) {
	const token = "clunky-slaw-catty-halt-revert-galore"
	for _, color := range []bool{false, true} {
		banner := renderTokenBanner(token, color)
		rows := bannerRows(banner)
		if len(rows) < 3 {
			t.Fatalf("color=%v: expected a framed box, got:\n%s", color, banner)
		}
		// Every framed row must have the same visible width, so the right
		// border lines up regardless of content length or centering.
		want := utf8.RuneCountInString(rows[0])
		for i, r := range rows {
			if got := utf8.RuneCountInString(r); got != want {
				t.Fatalf("color=%v: row %d width = %d, want %d\nrow: %q", color, i, got, want, r)
			}
		}
		if !strings.HasPrefix(rows[0], "╭") || !strings.HasSuffix(rows[0], "╮") {
			t.Fatalf("color=%v: bad top border %q", color, rows[0])
		}
		if last := rows[len(rows)-1]; !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
			t.Fatalf("color=%v: bad bottom border %q", color, last)
		}
		if !strings.Contains(banner, token) {
			t.Fatalf("color=%v: banner does not contain the token", color)
		}
	}
}

func TestRenderTokenBannerColorToggle(t *testing.T) {
	const token = "clunky-slaw-catty-halt-revert-galore"

	plain := renderTokenBanner(token, false)
	if strings.Contains(plain, "\033[") {
		t.Fatalf("color=false must not emit ANSI escapes:\n%q", plain)
	}

	colored := renderTokenBanner(token, true)
	if !strings.Contains(colored, ansiToken) {
		t.Fatalf("color=true must style the token with %q", ansiToken)
	}
	// Stripping the escapes must reproduce the plain rendering exactly.
	if got := ansiRE.ReplaceAllString(colored, ""); got != plain {
		t.Fatalf("colored banner without escapes should equal plain banner\n got: %q\nwant: %q", got, plain)
	}
}
