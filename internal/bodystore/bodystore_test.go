package bodystore

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestKeyMapping(t *testing.T) {
	cases := map[[2]string]string{
		{"", "sha256:abc"}:         "sha256/abc",
		{"straza", "sha256:abc"}:   "straza/sha256/abc",
		{"straza/", "sha256:abc"}:  "straza/sha256/abc",
		{"cell-eu/x", "sha256:ff"}: "cell-eu/x/sha256/ff",
	}
	for in, want := range cases {
		if got := key(in[0], in[1]); got != want {
			t.Errorf("key(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	if _, err := m.Get(ctx, "sha256:missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing body err = %v, want ErrNotFound", err)
	}
	if err := m.Put(ctx, "sha256:a", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ctx, "sha256:a", []byte("hello")); err != nil {
		t.Fatal(err) // idempotent
	}
	body, err := m.Get(ctx, "sha256:a")
	if err != nil || string(body) != "hello" {
		t.Fatalf("Get = %q, %v", body, err)
	}
	if m.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (content-addressed dedupe)", m.Len())
	}
}

// TestS3LiveRoundTrip validates the real minio-go path against a live
// S3-compatible endpoint. Gated: set STRAZA_TEST_S3_ENDPOINT (host:port,
// plain HTTP) with STRAZA_TEST_S3_ACCESS/SECRET and a pre-created bucket
// "straza-test", e.g. a local MinIO container. Skipped otherwise.
func TestS3LiveRoundTrip(t *testing.T) {
	endpoint := os.Getenv("STRAZA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("STRAZA_TEST_S3_ENDPOINT not set")
	}
	s, err := NewS3(S3Opts{
		Endpoint:  endpoint,
		Bucket:    "straza-test",
		Prefix:    "turns",
		AccessKey: os.Getenv("STRAZA_TEST_S3_ACCESS"),
		SecretKey: os.Getenv("STRAZA_TEST_S3_SECRET"),
		UseSSL:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, "sha256:live-test", []byte("transcript body")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	body, err := s.Get(ctx, "sha256:live-test")
	if err != nil || string(body) != "transcript body" {
		t.Fatalf("Get = %q, %v", body, err)
	}
	if _, err := s.Get(ctx, "sha256:definitely-absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent body err = %v, want ErrNotFound", err)
	}
}
