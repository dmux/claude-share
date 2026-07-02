// Package localfs handles the client's local project directory: building the
// manifest to upload (honoring .gitignore) and applying file changes returned
// by the server atomically.
package localfs

import (
	"os"
	"path/filepath"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/dmux/claude-share/internal/core/domain"
	"github.com/dmux/claude-share/internal/fsutil"
)

// Project is a handle to a local project root.
type Project struct {
	root        string
	ig          *gitignore.GitIgnore
	maxFileSize int64
	maxTotal    int64
}

// Open prepares a project at root, loading .gitignore and .claudeshareignore if
// present. maxFileSize/maxTotal are guards (0 = unlimited).
func Open(root string, maxFileSize, maxTotal int64) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, os.ErrInvalid
	}
	var lines []string
	for _, name := range []string{".gitignore", ".claudeshareignore"} {
		if b, err := os.ReadFile(filepath.Join(abs, name)); err == nil {
			lines = append(lines, splitLines(b)...)
		}
	}
	ig := gitignore.CompileIgnoreLines(lines...)
	return &Project{root: abs, ig: ig, maxFileSize: maxFileSize, maxTotal: maxTotal}, nil
}

// Root returns the absolute project root.
func (p *Project) Root() string { return p.root }

// Manifest builds the manifest of the project, skipping ignored paths.
func (p *Project) Manifest() (domain.Manifest, error) {
	return fsutil.WalkManifest(p.root, fsutil.WalkOptions{
		Ignore:       p.ignored,
		MaxFileSize:  p.maxFileSize,
		MaxTotalSize: p.maxTotal,
	})
}

func (p *Project) ignored(rel string, isDir bool) bool {
	if p.ig == nil {
		return false
	}
	return p.ig.MatchesPath(rel)
}

// ReadFile returns the content of a project-relative path, path-safe.
func (p *Project) ReadFile(rel string) ([]byte, error) {
	abs, err := fsutil.SafeJoin(p.root, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// Apply applies one file change from the server. For create/modify, content is
// written atomically (temp + rename). For delete, the file is removed.
func (p *Project) Apply(ch domain.FileChange, content []byte) error {
	abs, err := fsutil.SafeJoin(p.root, ch.Path)
	if err != nil {
		return err
	}
	if ch.Op == domain.ChangeDelete {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(ch.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".claude-share-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

func splitLines(b []byte) []string {
	var lines []string
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			line := string(b[start:i])
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, string(b[start:]))
	}
	return lines
}
