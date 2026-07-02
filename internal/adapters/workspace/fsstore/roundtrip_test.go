package fsstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmux/claude-share/internal/adapters/localfs"
	"github.com/dmux/claude-share/internal/adapters/workspace/fsstore"
	"github.com/dmux/claude-share/internal/fsutil"
)

// TestSyncRoundTrip exercises the full file path: client manifest -> blob upload
// -> server workspace materialize -> agent mutates workspace -> diff -> client
// applies the changes and ends up matching the workspace.
func TestSyncRoundTrip(t *testing.T) {
	clientDir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(clientDir, "README.md"), []byte("hello\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(clientDir, "keep.txt"), []byte("keep\n"), 0o644))

	proj, err := localfs.Open(clientDir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := proj.Manifest()
	if err != nil {
		t.Fatal(err)
	}

	// Upload every blob into the server store (simulating the sync protocol).
	blobs, err := fsstore.NewBlobStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range manifest.Entries {
		content, err := proj.ReadFile(e.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := blobs.Put(e.Hash, content); err != nil {
			t.Fatal(err)
		}
	}

	// Materialize the workspace and snapshot it (pre-turn).
	ws, err := fsstore.NewWorkspace(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Materialize(manifest, blobs); err != nil {
		t.Fatal(err)
	}
	before, err := ws.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the agent: create HELLO.md, modify README.md, delete keep.txt.
	must(t, os.WriteFile(filepath.Join(ws.Dir(), "HELLO.md"), []byte("hi there\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(ws.Dir(), "README.md"), []byte("hello\nworld\n"), 0o644))
	must(t, os.Remove(filepath.Join(ws.Dir(), "keep.txt")))

	after, err := ws.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	changes := fsutil.Diff(before, after)
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d: %+v", len(changes), changes)
	}

	// Apply the changes back to the client project.
	for _, ch := range changes {
		var content []byte
		if ch.Hash != "" {
			content, err = ws.ReadFile(ch.Path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := proj.Apply(ch, content); err != nil {
			t.Fatal(err)
		}
	}

	// The client tree must now equal the server workspace.
	clientM, err := proj.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(clientM.Entries) != len(after.Entries) {
		t.Fatalf("client has %d entries, workspace has %d", len(clientM.Entries), len(after.Entries))
	}
	wIdx := after.ByPath()
	for _, e := range clientM.Entries {
		if wIdx[e.Path].Hash != e.Hash {
			t.Errorf("hash mismatch for %s", e.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(clientDir, "keep.txt")); !os.IsNotExist(err) {
		t.Error("keep.txt should have been deleted on the client")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
