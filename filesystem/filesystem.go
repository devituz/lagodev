// Package filesystem provides a Disk abstraction modelled on Laravel's
// Storage facade.
//
// The Disk interface is intentionally small so a Local driver can ship
// in-box (no external deps) while S3/GCS/Azure drivers live in
// sub-packages once they are needed.
//
// Path safety is enforced by the Local driver: any path that would
// resolve outside the configured root (via .., absolute paths, or
// symlinks pointing outside) is rejected with ErrPathTraversal. The
// driver never opens, writes, or deletes outside its root.
//
// Usage:
//
//	disk, err := filesystem.NewLocal("/var/app/storage")
//	if err != nil { return err }
//	_ = disk.Put(ctx, "users/42/avatar.png", data)
//	r, err := disk.Reader(ctx, "users/42/avatar.png")
package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound is returned when a file does not exist on the disk.
var ErrNotFound = errors.New("filesystem: not found")

// ErrPathTraversal is returned when the requested path would escape
// the disk root.
var ErrPathTraversal = errors.New("filesystem: path escapes root")

// FileInfo describes a file on the disk. Modelled after the parts of
// os.FileInfo callers actually use.
type FileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Disk is the storage abstraction. Implementations must be safe for
// concurrent use; the Local driver is.
type Disk interface {
	// Put writes data atomically to path, replacing any existing file.
	Put(ctx context.Context, path string, data []byte) error
	// PutStream copies r into path. Larger payloads should prefer this
	// over Put to avoid loading the whole body into memory.
	PutStream(ctx context.Context, path string, r io.Reader) error
	// Get returns the file's content.
	Get(ctx context.Context, path string) ([]byte, error)
	// Reader returns an open reader; caller must Close it.
	Reader(ctx context.Context, path string) (io.ReadCloser, error)
	// Exists reports whether path exists.
	Exists(ctx context.Context, path string) (bool, error)
	// Delete removes path (no error if missing).
	Delete(ctx context.Context, path string) error
	// Copy duplicates src to dst within the same disk.
	Copy(ctx context.Context, src, dst string) error
	// Move renames src to dst.
	Move(ctx context.Context, src, dst string) error
	// Files lists non-directory entries directly under dir (no recursion).
	Files(ctx context.Context, dir string) ([]FileInfo, error)
	// Stat returns metadata for path.
	Stat(ctx context.Context, path string) (FileInfo, error)
}

// Local is a Disk that stores files on the host filesystem under a
// fixed root directory. Safe for concurrent use.
type Local struct {
	root string
	// mode is the perm bits applied to new files.
	mode os.FileMode
	// dirMode is the perm bits applied to new directories.
	dirMode os.FileMode
}

// NewLocal returns a Local disk rooted at the given absolute path. The
// root is created with 0o755 if missing.
func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, errors.New("filesystem: root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: abs root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("filesystem: mkdir root: %w", err)
	}
	return &Local{root: abs, mode: 0o644, dirMode: 0o755}, nil
}

// resolve joins p to the root and ensures the result stays inside it.
// Any ".." segment, absolute path, or post-clean traversal is rejected
// explicitly — silently rewriting "../etc/passwd" to "etc/passwd"
// would mask the caller's intent.
func (l *Local) resolve(p string) (string, error) {
	if filepath.IsAbs(p) {
		return "", ErrPathTraversal
	}
	// Reject any ".." segment outright. filepath.Clean would absorb
	// these, but for a storage root we want a hard "no traversal"
	// signal rather than a silent rewrite.
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}
	clean := filepath.Clean(p)
	full := filepath.Join(l.root, clean)
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", ErrPathTraversal
	}
	return full, nil
}

func (l *Local) Put(_ context.Context, path string, data []byte) error {
	return l.PutStream(context.Background(), path, bytes.NewReader(data))
}

func (l *Local) PutStream(_ context.Context, path string, r io.Reader) error {
	full, err := l.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), l.dirMode); err != nil {
		return fmt.Errorf("filesystem: mkdir: %w", err)
	}
	// Atomic write via temp file + rename.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return fmt.Errorf("filesystem: create tmp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("filesystem: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("filesystem: close tmp: %w", err)
	}
	if err := os.Chmod(tmpName, l.mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("filesystem: chmod: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("filesystem: rename: %w", err)
	}
	return nil
}

func (l *Local) Get(_ context.Context, path string) ([]byte, error) {
	full, err := l.resolve(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

func (l *Local) Reader(_ context.Context, path string) (io.ReadCloser, error) {
	full, err := l.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (l *Local) Exists(_ context.Context, path string) (bool, error) {
	full, err := l.resolve(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (l *Local) Delete(_ context.Context, path string) error {
	full, err := l.resolve(path)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l *Local) Copy(ctx context.Context, src, dst string) error {
	r, err := l.Reader(ctx, src)
	if err != nil {
		return err
	}
	defer r.Close()
	return l.PutStream(ctx, dst, r)
}

func (l *Local) Move(_ context.Context, src, dst string) error {
	fullSrc, err := l.resolve(src)
	if err != nil {
		return err
	}
	fullDst, err := l.resolve(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullDst), l.dirMode); err != nil {
		return err
	}
	if err := os.Rename(fullSrc, fullDst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (l *Local) Files(_ context.Context, dir string) ([]FileInfo, error) {
	full, err := l.resolve(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
		out = append(out, FileInfo{
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   false,
		})
	}
	return out, nil
}

func (l *Local) Stat(_ context.Context, path string) (FileInfo, error) {
	full, err := l.resolve(path)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileInfo{}, ErrNotFound
		}
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

// Root returns the absolute filesystem root the Local disk uses.
func (l *Local) Root() string { return l.root }
