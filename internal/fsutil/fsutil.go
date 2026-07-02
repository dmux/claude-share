// Package fsutil provides filesystem walking, content hashing, and manifest
// helpers shared by the server workspace and the client's local project.
package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dmux/claude-share/internal/core/domain"
)

// DefaultIgnoredDirs are never synced regardless of .gitignore.
var DefaultIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".claude":      true, // per-project Claude state, not project content
}

// WalkOptions configures a manifest walk.
type WalkOptions struct {
	// Ignore, if non-nil, reports whether a slash-relative path should be
	// skipped. Directories are passed with a trailing slash.
	Ignore func(relPath string, isDir bool) bool
	// MaxFileSize rejects any single file larger than this (0 = unlimited).
	MaxFileSize int64
	// MaxTotalSize rejects a tree whose total exceeds this (0 = unlimited).
	MaxTotalSize int64
}

// HashContent returns the lowercase hex SHA-256 of b.
func HashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// WalkManifest builds a manifest of the regular files under root. Symlinks are
// skipped. Paths are slash-separated and relative to root. Entries are sorted by
// path for deterministic output (stable diffs and hashes).
func WalkManifest(root string, opts WalkOptions) (domain.Manifest, error) {
	var entries []domain.FileEntry
	var total int64

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if DefaultIgnoredDirs[d.Name()] {
				return fs.SkipDir
			}
			if opts.Ignore != nil && opts.Ignore(rel+"/", true) {
				return fs.SkipDir
			}
			return nil
		}
		// Only regular files; skip symlinks, sockets, devices.
		if !d.Type().IsRegular() {
			return nil
		}
		if opts.Ignore != nil && opts.Ignore(rel, false) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if opts.MaxFileSize > 0 && info.Size() > opts.MaxFileSize {
			return fmt.Errorf("fsutil: %s exceeds max file size (%d > %d)", rel, info.Size(), opts.MaxFileSize)
		}
		total += info.Size()
		if opts.MaxTotalSize > 0 && total > opts.MaxTotalSize {
			return fmt.Errorf("fsutil: project exceeds max total size (%d)", opts.MaxTotalSize)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, domain.FileEntry{
			Path: rel,
			Hash: HashContent(content),
			Size: info.Size(),
			Mode: domain.FileMode(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return domain.Manifest{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return domain.Manifest{Entries: entries}, nil
}

// Diff computes the changes needed to turn oldM into newM.
func Diff(oldM, newM domain.Manifest) []domain.FileChange {
	oldIdx := oldM.ByPath()
	newIdx := newM.ByPath()

	var changes []domain.FileChange
	for _, e := range newM.Entries {
		prev, ok := oldIdx[e.Path]
		switch {
		case !ok:
			changes = append(changes, domain.FileChange{Op: domain.ChangeCreate, Path: e.Path, Hash: e.Hash, Mode: e.Mode})
		case prev.Hash != e.Hash || prev.Mode != e.Mode:
			changes = append(changes, domain.FileChange{Op: domain.ChangeModify, Path: e.Path, Hash: e.Hash, Mode: e.Mode})
		}
	}
	for _, e := range oldM.Entries {
		if _, ok := newIdx[e.Path]; !ok {
			changes = append(changes, domain.FileChange{Op: domain.ChangeDelete, Path: e.Path})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// SafeJoin joins root and a slash-relative path, guaranteeing the result stays
// inside root. It rejects absolute paths and any "../" escape.
func SafeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("fsutil: empty path")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fsutil: unsafe path %q", rel)
	}
	joined := filepath.Join(root, clean)
	// Defense in depth: verify containment after the join.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joinedAbs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if joinedAbs != rootAbs && !strings.HasPrefix(joinedAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("fsutil: path %q escapes root", rel)
	}
	return joined, nil
}
