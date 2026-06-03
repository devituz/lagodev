package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDisk(t *testing.T) *Local {
	t.Helper()
	d, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return d
}

func TestNewLocal_CreatesRootIfMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "storage")
	d, err := NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	if _, err := os.Stat(d.Root()); err != nil {
		t.Fatalf("root not created: %v", err)
	}
}

func TestNewLocal_RejectsEmpty(t *testing.T) {
	if _, err := NewLocal(""); err == nil {
		t.Fatal("must reject empty root")
	}
}

func TestPutGet_Roundtrip(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	if err := d.Put(ctx, "a.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := d.Get(ctx, "a.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("Get = (%q, %v)", got, err)
	}
}

func TestPut_CreatesIntermediateDirs(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	if err := d.Put(ctx, "users/42/profile/avatar.png", []byte("png")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, _ := d.Exists(ctx, "users/42/profile/avatar.png")
	if !ok {
		t.Fatal("nested path must exist after Put")
	}
}

func TestPutStream_LargePayload(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	data := bytes.Repeat([]byte("x"), 1<<16)
	if err := d.PutStream(ctx, "big.bin", bytes.NewReader(data)); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	got, _ := d.Get(ctx, "big.bin")
	if len(got) != len(data) {
		t.Fatalf("size mismatch: %d vs %d", len(got), len(data))
	}
}

func TestGet_NotFound(t *testing.T) {
	_, err := newDisk(t).Get(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestReader_StreamingNotFound(t *testing.T) {
	_, err := newDisk(t).Reader(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestReader_DrainsContent(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	_ = d.Put(ctx, "f", []byte("content"))
	r, err := d.Reader(ctx, "f")
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	if string(b) != "content" {
		t.Fatalf("got %q", b)
	}
}

func TestDelete_NoopOnMissing(t *testing.T) {
	if err := newDisk(t).Delete(context.Background(), "nope"); err != nil {
		t.Fatalf("Delete on missing must not error, got %v", err)
	}
}

func TestCopyAndMove(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	_ = d.Put(ctx, "src.txt", []byte("hi"))

	if err := d.Copy(ctx, "src.txt", "dst.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	a, _ := d.Get(ctx, "src.txt")
	b, _ := d.Get(ctx, "dst.txt")
	if string(a) != "hi" || string(b) != "hi" {
		t.Fatalf("after Copy: src=%q dst=%q", a, b)
	}

	if err := d.Move(ctx, "src.txt", "moved.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if ok, _ := d.Exists(ctx, "src.txt"); ok {
		t.Fatal("src must be gone after Move")
	}
	if ok, _ := d.Exists(ctx, "moved.txt"); !ok {
		t.Fatal("dst must exist after Move")
	}
}

func TestFiles_ListsDirectChildren(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	_ = d.Put(ctx, "a.txt", []byte("1"))
	_ = d.Put(ctx, "b.txt", []byte("2"))
	_ = d.Put(ctx, "sub/c.txt", []byte("3")) // should NOT appear in root listing
	files, err := d.Files(ctx, ".")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files in root, got %d: %+v", len(files), files)
	}
}

func TestStat_ReturnsSize(t *testing.T) {
	ctx := context.Background()
	d := newDisk(t)
	_ = d.Put(ctx, "f", []byte("12345"))
	info, err := d.Stat(ctx, "f")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != 5 || info.IsDir {
		t.Fatalf("info = %+v", info)
	}
}

func TestPathTraversal_Rejected(t *testing.T) {
	d := newDisk(t)
	ctx := context.Background()
	bad := []string{
		"../etc/passwd",
		"foo/../../etc/passwd",
		"/absolute/path",
		"/etc/passwd",
	}
	for _, p := range bad {
		err := d.Put(ctx, p, []byte("x"))
		if !errors.Is(err, ErrPathTraversal) {
			t.Fatalf("Put(%q) must return ErrPathTraversal, got %v", p, err)
		}
		_, err = d.Get(ctx, p)
		if !errors.Is(err, ErrPathTraversal) {
			t.Fatalf("Get(%q) must return ErrPathTraversal, got %v", p, err)
		}
	}
}

func TestPathTraversal_CleanInternalDots(t *testing.T) {
	// "a/./b" and "a/b/" should both resolve to the same file under
	// the root, not be rejected.
	d := newDisk(t)
	ctx := context.Background()
	if err := d.Put(ctx, "a/./b.txt", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, _ := d.Exists(ctx, "a/b.txt")
	if !ok {
		t.Fatal("path with /./ must clean to plain join")
	}
}

func TestAtomicWrite_NoPartialOnError(t *testing.T) {
	d := newDisk(t)
	ctx := context.Background()
	_ = d.Put(ctx, "f.txt", []byte("original"))
	// Use a reader that errors midstream
	br := &brokenReader{n: 3}
	err := d.PutStream(ctx, "f.txt", br)
	if err == nil {
		t.Fatal("expected error from broken reader")
	}
	got, _ := d.Get(ctx, "f.txt")
	if string(got) != "original" {
		t.Fatalf("original must survive failed write, got %q", got)
	}
}

type brokenReader struct{ n int }

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		return 0, errors.New("boom")
	}
	for i := 0; i < b.n && i < len(p); i++ {
		p[i] = 'x'
	}
	out := b.n
	b.n = 0
	return out, errors.New("boom")
}

// Sanity: directory entries returned by Files use forward slashes.
func TestFiles_PathsUseForwardSlash(t *testing.T) {
	d := newDisk(t)
	ctx := context.Background()
	_ = d.Put(ctx, "a.txt", []byte("1"))
	files, _ := d.Files(ctx, ".")
	for _, f := range files {
		if strings.Contains(f.Path, "\\") {
			t.Fatalf("Path must use /, got %q", f.Path)
		}
	}
}
