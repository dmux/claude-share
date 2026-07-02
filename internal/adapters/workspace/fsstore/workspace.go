package fsstore

import (
	"os"
	"path/filepath"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/core/ports"
	"github.com/dmux/claude-share/internal/fsutil"
)

// Workspace materializes and inspects one session's project tree on disk.
type Workspace struct {
	dir string
}

// NewWorkspace roots a workspace at dir, creating it fresh (any prior contents
// are removed so a session always starts from the uploaded snapshot).
func NewWorkspace(dir string) (*Workspace, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{dir: dir}, nil
}

// Dir returns the absolute workspace path Claude runs in.
func (w *Workspace) Dir() string { return w.dir }

// Materialize writes every manifest entry into the workspace, pulling content
// from the blob store.
func (w *Workspace) Materialize(manifest domain.Manifest, blobs ports.BlobStore) error {
	for _, e := range manifest.Entries {
		dst, err := fsutil.SafeJoin(w.dir, e.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		content, err := blobs.Get(e.Hash)
		if err != nil {
			return err
		}
		mode := os.FileMode(e.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dst, content, mode); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot computes the current manifest of the workspace. Default ignored dirs
// (.git, node_modules) are excluded so agent-created build artifacts are not
// synced back.
func (w *Workspace) Snapshot() (domain.Manifest, error) {
	return fsutil.WalkManifest(w.dir, fsutil.WalkOptions{})
}

// ReadFile returns the content of a workspace-relative path, path-safe.
func (w *Workspace) ReadFile(path string) ([]byte, error) {
	p, err := fsutil.SafeJoin(w.dir, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Remove deletes the workspace tree.
func (w *Workspace) Remove() error { return os.RemoveAll(w.dir) }
