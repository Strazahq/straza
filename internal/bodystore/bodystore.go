// Package bodystore holds transcript turn BODIES outside the relational
// store, content-addressed by the turn's full-content hash. Identical
// prompts across a fleet store once, the key itself witnesses the content,
// and retention is the bucket's lifecycle rule rather than a DELETE. It is
// enterprise and opt-in: the default keeps bodies inline in the store and
// never touches this package.
package bodystore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound reports a body the store does not hold. Under a live turn
// row this is an INTEGRITY finding (a row must only ever reference a durable
// body), surfaced loudly by the read path, never silently.
var ErrNotFound = errors.New("bodystore: body not found")

// Store holds turn bodies by content hash.
type Store interface {
	// Put stores body under hash (idempotent: same hash, same content).
	Put(ctx context.Context, hash string, body []byte) error
	// Get returns the body for hash, or ErrNotFound.
	Get(ctx context.Context, hash string) ([]byte, error)
}

// key maps a contentHash ("sha256:<hex>") onto an object key. The algorithm
// prefix becomes a directory so future hash algorithms cannot collide.
func key(prefix, hash string) string {
	h := strings.ReplaceAll(hash, ":", "/")
	if prefix == "" {
		return h
	}
	return strings.TrimSuffix(prefix, "/") + "/" + h
}

// --- S3-compatible implementation (minio-go: pure Go, works against AWS
// S3, MinIO, Ceph and GCS interop) ---

// S3Opts configures the S3-compatible store.
type S3Opts struct {
	Endpoint  string // host[:port], no scheme
	Bucket    string
	Prefix    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// S3 is the S3-compatible Store.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3 builds the store; it does not dial (first Put/Get does).
func NewS3(o S3Opts) (*S3, error) {
	c, err := minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
		Region: o.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("bodystore: %w", err)
	}
	return &S3{client: c, bucket: o.Bucket, prefix: o.Prefix}, nil
}

// Put implements Store. Unconditional put: content-addressed keys make an
// overwrite a byte-identical no-op, and one round trip beats stat-then-put.
func (s *S3) Put(ctx context.Context, hash string, body []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key(s.prefix, hash),
		bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "text/plain; charset=utf-8"})
	if err != nil {
		return fmt.Errorf("bodystore: put %s: %w", hash, err)
	}
	return nil
}

// Get implements Store.
func (s *S3) Get(ctx context.Context, hash string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key(s.prefix, hash), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("bodystore: get %s: %w", hash, err)
	}
	defer func() { _ = obj.Close() }()
	body, err := io.ReadAll(obj)
	if err != nil {
		var mErr minio.ErrorResponse
		if errors.As(err, &mErr) && mErr.Code == "NoSuchKey" {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
		}
		return nil, fmt.Errorf("bodystore: read %s: %w", hash, err)
	}
	return body, nil
}

// --- in-memory implementation (tests and the fake-integration seam) ---

// Memory is a map-backed Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

// Put implements Store.
func (s *Memory) Put(_ context.Context, hash string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[hash] = append([]byte(nil), body...)
	return nil
}

// Get implements Store.
func (s *Memory) Get(_ context.Context, hash string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[hash]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	return append([]byte(nil), b...), nil
}

// Len reports stored bodies (tests).
func (s *Memory) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
