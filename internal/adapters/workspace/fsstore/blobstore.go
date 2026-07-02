// Package fsstore implements the BlobStore and Workspace driven ports on the
// local filesystem, server-side.
package fsstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BlobStore is a content-addressed store: files are named by the lowercase hex
// SHA-256 of their content, so identical content is stored once and shared
// across sessions.
type BlobStore struct {
	dir string
}

// NewBlobStore roots a content store at dir, creating it if needed.
func NewBlobStore(dir string) (*BlobStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &BlobStore{dir: dir}, nil
}

func (b *BlobStore) path(hash string) string { return filepath.Join(b.dir, hash) }

// Has reports whether the store already holds content for hash.
func (b *BlobStore) Has(hash string) (bool, error) {
	if !validHash(hash) {
		return false, fmt.Errorf("blobstore: invalid hash %q", hash)
	}
	_, err := os.Stat(b.path(hash))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Put stores content, verifying it matches the claimed hash. Writes are atomic
// (temp file + rename) so concurrent Puts of the same blob are safe.
func (b *BlobStore) Put(hash string, content []byte) error {
	if !validHash(hash) {
		return fmt.Errorf("blobstore: invalid hash %q", hash)
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != hash {
		return fmt.Errorf("blobstore: content hash %s does not match claimed %s", got, hash)
	}
	tmp, err := os.CreateTemp(b.dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, b.path(hash))
}

// Get returns the content for hash.
func (b *BlobStore) Get(hash string) ([]byte, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("blobstore: invalid hash %q", hash)
	}
	return os.ReadFile(b.path(hash))
}

// validHash checks a string is a 64-char lowercase hex SHA-256.
func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
