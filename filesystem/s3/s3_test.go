package s3

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devituz/lagodev/filesystem"
)

// TestNew_ValidatesConfig is the cheap unit-only check we always run.
// Real S3 operations are exercised by TestIntegration_* below, which
// is skipped unless S3_ENDPOINT is set so CI stays hermetic.
func TestNew_ValidatesConfig(t *testing.T) {
	if _, err := New(Config{Bucket: "x"}); err == nil {
		t.Fatal("missing Endpoint must error")
	}
	if _, err := New(Config{Endpoint: "x:9000"}); err == nil {
		t.Fatal("missing Bucket must error")
	}
}

func TestNew_DefaultRegion(t *testing.T) {
	d, err := New(Config{Endpoint: "minio.local:9000", Bucket: "x", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d == nil || d.cli == nil {
		t.Fatal("client not constructed")
	}
}

func TestKeyFor_PrefixApplied(t *testing.T) {
	d, _ := New(Config{Endpoint: "minio.local:9000", Bucket: "x", AccessKey: "k", SecretKey: "s", Prefix: "app/uploads"})
	got := d.keyFor("users/42/avatar.png")
	want := "app/uploads/users/42/avatar.png"
	if got != want {
		t.Fatalf("keyFor = %q, want %q", got, want)
	}
}

func TestKeyFor_NoPrefix(t *testing.T) {
	d, _ := New(Config{Endpoint: "x:9000", Bucket: "x", AccessKey: "k", SecretKey: "s"})
	if got := d.keyFor("/a/b.txt"); got != "a/b.txt" {
		t.Fatalf("keyFor = %q", got)
	}
}

// Compile-time guard: confirms *Disk satisfies filesystem.Disk.
var _ filesystem.Disk = (*Disk)(nil)

// --- Integration tests --------------------------------------------------
//
// To run these, start MinIO locally:
//
//	docker run --rm -p 9000:9000 -p 9001:9001 \
//	    -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio123 \
//	    minio/minio server /data --console-address ":9001"
//
// Then export:
//
//	export S3_ENDPOINT=localhost:9000
//	export S3_KEY=minio
//	export S3_SECRET=minio123
//	export S3_BUCKET=lagodev-test   # must exist
//	go test -v ./filesystem/s3/

func integrationDisk(t *testing.T) (*Disk, context.Context) {
	t.Helper()
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_ENDPOINT not set — skipping integration test")
	}
	d, err := New(Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("S3_KEY"),
		SecretKey: os.Getenv("S3_SECRET"),
		Bucket:    osGetenvDefault("S3_BUCKET", "lagodev-test"),
		UseSSL:    os.Getenv("S3_SSL") == "1",
		Prefix:    "test/" + time.Now().Format("20060102-150405"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return d, ctx
}

func osGetenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestIntegration_PutGet(t *testing.T) {
	d, ctx := integrationDisk(t)
	body := []byte("hello s3")
	if err := d.Put(ctx, "hello.txt", body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), "hello.txt") })

	got, err := d.Get(ctx, "hello.txt")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get = (%q, %v)", got, err)
	}
}

func TestIntegration_NotFound(t *testing.T) {
	d, ctx := integrationDisk(t)
	_, err := d.Get(ctx, "definitely-not-there.txt")
	if !errors.Is(err, filesystem.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestIntegration_ExistsAndStat(t *testing.T) {
	d, ctx := integrationDisk(t)
	_ = d.Put(ctx, "stat.txt", []byte("12345"))
	t.Cleanup(func() { _ = d.Delete(context.Background(), "stat.txt") })

	ok, _ := d.Exists(ctx, "stat.txt")
	if !ok {
		t.Fatal("Exists must be true after Put")
	}
	info, err := d.Stat(ctx, "stat.txt")
	if err != nil || info.Size != 5 {
		t.Fatalf("Stat = (%+v, %v)", info, err)
	}
}

func TestIntegration_DeleteAndList(t *testing.T) {
	d, ctx := integrationDisk(t)
	_ = d.Put(ctx, "list/a.txt", []byte("a"))
	_ = d.Put(ctx, "list/b.txt", []byte("b"))
	t.Cleanup(func() {
		_ = d.Delete(context.Background(), "list/a.txt")
		_ = d.Delete(context.Background(), "list/b.txt")
	})

	files, err := d.Files(ctx, "list")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(files), files)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".txt") {
			t.Fatalf("unexpected key in listing: %s", f.Path)
		}
	}
}

func TestIntegration_CopyMove(t *testing.T) {
	d, ctx := integrationDisk(t)
	_ = d.Put(ctx, "cm/src.txt", []byte("hi"))
	t.Cleanup(func() {
		_ = d.Delete(context.Background(), "cm/src.txt")
		_ = d.Delete(context.Background(), "cm/dst.txt")
		_ = d.Delete(context.Background(), "cm/moved.txt")
	})

	if err := d.Copy(ctx, "cm/src.txt", "cm/dst.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	srcOK, _ := d.Exists(ctx, "cm/src.txt")
	dstOK, _ := d.Exists(ctx, "cm/dst.txt")
	if !srcOK || !dstOK {
		t.Fatalf("after Copy: src=%v dst=%v", srcOK, dstOK)
	}

	if err := d.Move(ctx, "cm/src.txt", "cm/moved.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	srcOK, _ = d.Exists(ctx, "cm/src.txt")
	movedOK, _ := d.Exists(ctx, "cm/moved.txt")
	if srcOK || !movedOK {
		t.Fatalf("after Move: src=%v moved=%v", srcOK, movedOK)
	}
}
