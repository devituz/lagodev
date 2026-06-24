// Package s3 implements the filesystem.Disk interface against any
// S3-compatible object store: AWS S3, MinIO, DigitalOcean Spaces,
// Cloudflare R2, Wasabi, Backblaze B2 (S3 mode), Ceph RGW.
//
// The driver is a separate Go module so the core lagodev module stays
// dependency-free:
//
//	go get github.com/devituz/lagodev/filesystem/s3@latest
//
// Usage:
//
//	disk, err := s3.New(s3.Config{
//	    Endpoint:  "minio.local:9000",  // host:port — no scheme
//	    AccessKey: os.Getenv("S3_KEY"),
//	    SecretKey: os.Getenv("S3_SECRET"),
//	    Bucket:    "uploads",
//	    Region:    "us-east-1",
//	    UseSSL:    false,                // true for AWS S3, false for local MinIO
//	})
//	if err != nil { return err }
//	_ = disk.Put(ctx, "users/42/avatar.png", data)
//
// MinIO note: MinIO speaks the same protocol as S3, so the same code
// works without changes; just point Endpoint at your MinIO host and
// set UseSSL to match how MinIO is exposed.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/devituz/lagodev/filesystem"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config holds connection settings for an S3-compatible endpoint.
type Config struct {
	// Endpoint is "host:port" (no scheme). For AWS S3 use
	// "s3.us-east-1.amazonaws.com". For MinIO use "minio.local:9000"
	// or wherever your instance listens.
	Endpoint  string
	AccessKey string
	SecretKey string
	// Bucket is the bucket every operation is scoped to.
	Bucket string
	// Region for signing. Defaults to "us-east-1" — MinIO accepts this.
	Region string
	// UseSSL toggles HTTPS. AWS: true. MinIO over HTTP: false.
	UseSSL bool
	// Prefix optionally prepends a key prefix to every operation so
	// multiple lagodev apps can share a bucket without colliding.
	Prefix string
}

// Disk implements filesystem.Disk against an S3-compatible store.
type Disk struct {
	cli    *minio.Client
	bucket string
	prefix string
}

// New constructs a Disk. The bucket is NOT created here; ensure it
// exists out-of-band (e.g. with `mc mb` or the MinIO console).
func New(cfg Config) (*Disk, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("s3: Endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("s3: Bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &Disk{
		cli:    cli,
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
	}, nil
}

// Static compile-time assertion: *Disk satisfies filesystem.Disk.
var _ filesystem.Disk = (*Disk)(nil)

// keyFor maps a caller path to an object key, applying the configured
// prefix. It enforces the same no-traversal contract as the Local
// driver: absolute paths and any ".." segment are rejected with
// filesystem.ErrPathTraversal rather than silently cleaned, so a key
// can never escape the configured prefix.
func (d *Disk) keyFor(p string) (string, error) {
	if path.IsAbs(p) {
		return "", filesystem.ErrPathTraversal
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", filesystem.ErrPathTraversal
		}
	}
	clean := strings.TrimLeft(path.Clean(p), "/")
	if d.prefix == "" {
		return clean, nil
	}
	return d.prefix + "/" + clean, nil
}

func (d *Disk) Put(ctx context.Context, p string, data []byte) error {
	return d.PutStream(ctx, p, bytes.NewReader(data))
}

func (d *Disk) PutStream(ctx context.Context, p string, r io.Reader) error {
	key, err := d.keyFor(p)
	if err != nil {
		return err
	}
	// Size unknown → use -1, minio streams in 5 MiB parts.
	_, err = d.cli.PutObject(ctx, d.bucket, key, r, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}
	return nil
}

func (d *Disk) Get(ctx context.Context, p string) ([]byte, error) {
	r, err := d.Reader(ctx, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (d *Disk) Reader(ctx context.Context, p string) (io.ReadCloser, error) {
	key, err := d.keyFor(p)
	if err != nil {
		return nil, err
	}
	obj, err := d.cli.GetObject(ctx, d.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	// minio's GetObject is lazy — the GET happens on first Read.
	// Probe stat so we can return ErrNotFound up-front.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, filesystem.ErrNotFound
		}
		return nil, fmt.Errorf("s3: stat %s: %w", key, err)
	}
	return obj, nil
}

func (d *Disk) Exists(ctx context.Context, p string) (bool, error) {
	key, err := d.keyFor(p)
	if err != nil {
		return false, err
	}
	_, err = d.cli.StatObject(ctx, d.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

func (d *Disk) Delete(ctx context.Context, p string) error {
	key, err := d.keyFor(p)
	if err != nil {
		return err
	}
	err = d.cli.RemoveObject(ctx, d.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3: delete: %w", err)
	}
	return nil
}

func (d *Disk) Copy(ctx context.Context, src, dst string) error {
	srcKey, err := d.keyFor(src)
	if err != nil {
		return err
	}
	dstKey, err := d.keyFor(dst)
	if err != nil {
		return err
	}
	_, err = d.cli.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: d.bucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: d.bucket, Object: srcKey},
	)
	if err != nil {
		if isNotFound(err) {
			return filesystem.ErrNotFound
		}
		return fmt.Errorf("s3: copy: %w", err)
	}
	return nil
}

func (d *Disk) Move(ctx context.Context, src, dst string) error {
	if err := d.Copy(ctx, src, dst); err != nil {
		return err
	}
	return d.Delete(ctx, src)
}

func (d *Disk) Files(ctx context.Context, dir string) ([]filesystem.FileInfo, error) {
	prefix, err := d.keyFor(dir)
	if err != nil {
		return nil, err
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	ch := d.cli.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
	})
	var out []filesystem.FileInfo
	for obj := range ch {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3: list: %w", obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue // directory marker
		}
		rel := strings.TrimPrefix(obj.Key, d.prefix+"/")
		out = append(out, filesystem.FileInfo{
			Path:    rel,
			Size:    obj.Size,
			ModTime: obj.LastModified,
			IsDir:   false,
		})
	}
	return out, nil
}

func (d *Disk) Stat(ctx context.Context, p string) (filesystem.FileInfo, error) {
	key, err := d.keyFor(p)
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	info, err := d.cli.StatObject(ctx, d.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return filesystem.FileInfo{}, filesystem.ErrNotFound
		}
		return filesystem.FileInfo{}, err
	}
	return filesystem.FileInfo{
		Path:    p,
		Size:    info.Size,
		ModTime: info.LastModified,
		IsDir:   false,
	}, nil
}

// isNotFound returns true for the minio "NoSuchKey" error response.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	er := minio.ToErrorResponse(err)
	return er.Code == "NoSuchKey" || er.Code == "NotFound"
}
