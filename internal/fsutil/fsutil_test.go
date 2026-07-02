package fsutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmux/claude-share/internal/core/domain"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkManifestSkipsDefaultDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "pkg/util.go", "package pkg")
	writeFile(t, root, ".git/config", "gitstuff")
	writeFile(t, root, "node_modules/left-pad/index.js", "module.exports=1")

	m, err := WalkManifest(root, WalkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paths := m.ByPath()
	if _, ok := paths["main.go"]; !ok {
		t.Error("expected main.go")
	}
	if _, ok := paths["pkg/util.go"]; !ok {
		t.Error("expected pkg/util.go")
	}
	if _, ok := paths[".git/config"]; ok {
		t.Error(".git must be skipped")
	}
	if _, ok := paths["node_modules/left-pad/index.js"]; ok {
		t.Error("node_modules must be skipped")
	}
}

func TestWalkManifestDeterministicAndHashed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "b.txt", "bbb")
	writeFile(t, root, "a.txt", "aaa")

	m, err := WalkManifest(root, WalkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 || m.Entries[0].Path != "a.txt" || m.Entries[1].Path != "b.txt" {
		t.Fatalf("entries not sorted: %+v", m.Entries)
	}
	if m.Entries[0].Hash != HashContent([]byte("aaa")) {
		t.Fatal("wrong hash for a.txt")
	}
}

func TestMaxFileSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "big.bin", "0123456789")
	if _, err := WalkManifest(root, WalkOptions{MaxFileSize: 5}); err == nil {
		t.Fatal("expected max file size error")
	}
}

func TestDiff(t *testing.T) {
	old := domain.Manifest{Entries: []domain.FileEntry{
		{Path: "keep.txt", Hash: "h1"},
		{Path: "change.txt", Hash: "h2"},
		{Path: "gone.txt", Hash: "h3"},
	}}
	next := domain.Manifest{Entries: []domain.FileEntry{
		{Path: "keep.txt", Hash: "h1"},
		{Path: "change.txt", Hash: "h2new"},
		{Path: "new.txt", Hash: "h4"},
	}}
	changes := Diff(old, next)
	got := map[string]domain.ChangeOp{}
	for _, c := range changes {
		got[c.Path] = c.Op
	}
	want := map[string]domain.ChangeOp{
		"change.txt": domain.ChangeModify,
		"new.txt":    domain.ChangeCreate,
		"gone.txt":   domain.ChangeDelete,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for p, op := range want {
		if got[p] != op {
			t.Errorf("%s: got %s want %s", p, got[p], op)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		rel  string
		ok   bool
	}{
		{"a/b.txt", true},
		{"./a.txt", true},
		{"../escape.txt", false},
		{"a/../../escape.txt", false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, c := range cases {
		_, err := SafeJoin(root, c.rel)
		if c.ok && err != nil {
			t.Errorf("SafeJoin(%q) unexpected error: %v", c.rel, err)
		}
		if !c.ok && err == nil {
			t.Errorf("SafeJoin(%q) expected error", c.rel)
		}
	}
}
