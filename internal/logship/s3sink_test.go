package logship

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// TestS3SinkKeyPrefix pins the pure key-composition and prefix-normalization
// logic — the part of the S3 sink that runs without a store. A configured prefix
// is normalized to end in "/" so key() joins cleanly, and an empty prefix is a
// no-op. List reverses this (TrimPrefix), so a wrong normalization here would
// silently corrupt both the written keys and the names Replay reads back.
func TestS3SinkKeyPrefix(t *testing.T) {
	cases := []struct {
		prefix, name, want string
	}{
		{"", "segment-0000000001", "segment-0000000001"},
		{"logs", "segment-0000000001", "logs/segment-0000000001"},
		{"logs/", "segment-0000000001", "logs/segment-0000000001"},
		{"a/b", "s", "a/b/s"},
	}
	for _, c := range cases {
		s, err := NewS3Sink(S3Config{Endpoint: "s3.example.com", Bucket: "b", AccessKey: "a", SecretKey: "s", Prefix: c.prefix})
		if err != nil {
			t.Fatalf("NewS3Sink(prefix=%q): %v", c.prefix, err)
		}
		if got := s.key(c.name); got != c.want {
			t.Errorf("prefix %q: key(%q) = %q, want %q", c.prefix, c.name, got, c.want)
		}
	}
}

// TestS3SinkRoundTrip is the S3 integration test: it exercises Put/Get/List
// against a real S3-compatible store, gated on TOISE_TEST_S3_ENDPOINT so it runs
// only where a MinIO/S3 is provisioned (the soak cluster / a nightly job) and is
// skipped in normal CI. It proves the property Replay relies on: List returns the
// written names, prefix-stripped, in lexical (= sequence) order regardless of the
// order they were Put.
//
// Env: TOISE_TEST_S3_ENDPOINT (host:port), TOISE_TEST_S3_BUCKET,
// TOISE_TEST_S3_ACCESS_KEY, TOISE_TEST_S3_SECRET_KEY, optional TOISE_TEST_S3_SSL=1.
func TestS3SinkRoundTrip(t *testing.T) {
	endpoint := os.Getenv("TOISE_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TOISE_TEST_S3_ENDPOINT (and bucket/keys) to run the S3 integration test")
	}
	cfg := S3Config{
		Endpoint:  endpoint,
		Bucket:    os.Getenv("TOISE_TEST_S3_BUCKET"),
		AccessKey: os.Getenv("TOISE_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TOISE_TEST_S3_SECRET_KEY"),
		UseSSL:    os.Getenv("TOISE_TEST_S3_SSL") == "1",
		// Isolate this run under its own prefix so a shared bucket stays clean.
		Prefix: "toise-test/" + t.Name(),
	}
	sink, err := NewS3Sink(cfg)
	if err != nil {
		t.Fatalf("NewS3Sink: %v", err)
	}
	ctx := context.Background()

	// Put out of lexical order; List must still return them sorted.
	segs := map[string][]byte{
		"segment-0000000002": []byte("two"),
		"segment-0000000000": []byte("zero"),
		"segment-0000000001": []byte("one"),
	}
	for name, data := range segs {
		if perr := sink.Put(ctx, name, data); perr != nil {
			t.Fatalf("Put %s: %v", name, perr)
		}
	}

	names, err := sink.List(ctx, "segment-")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"segment-0000000000", "segment-0000000001", "segment-0000000002"}
	if len(names) != len(want) {
		t.Fatalf("List = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q (order/prefix-strip broken)", i, names[i], want[i])
		}
	}

	got, err := sink.Get(ctx, "segment-0000000001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("one")) {
		t.Errorf("Get round-trip = %q, want %q", got, "one")
	}
}
