package logship

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config configures an S3Sink. Endpoint is host[:port] without a scheme
// (e.g. s3.amazonaws.com or minio.internal:9000); UseSSL picks https. The same
// shape targets AWS S3 and any S3-compatible store (MinIO, Ceph, R2, …), which
// is the point — one config, every backend. AccessKey/SecretKey are secrets and
// come from the environment, never a flag or the config file.
type S3Config struct {
	Endpoint  string
	Bucket    string
	Region    string // optional; many S3-compatible stores ignore it
	Prefix    string // optional key prefix under the bucket
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// S3Sink ships segments to an S3-compatible object store via minio-go (the
// market-standard client; no hand-rolled signing). Satisfies Sink.
type S3Sink struct {
	client *minio.Client
	bucket string
	prefix string // normalized to end with "/" when non-empty
}

var _ Sink = (*S3Sink)(nil)

// NewS3Sink builds an S3Sink. It validates the required fields and constructs the
// client, but does not contact the store (the first Put/List will surface
// connectivity or auth errors with context).
func NewS3Sink(cfg S3Config) (*S3Sink, error) {
	switch {
	case cfg.Endpoint == "":
		return nil, fmt.Errorf("logship s3: endpoint is required")
	case cfg.Bucket == "":
		return nil, fmt.Errorf("logship s3: bucket is required")
	case cfg.AccessKey == "" || cfg.SecretKey == "":
		return nil, fmt.Errorf("logship s3: access key and secret key are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("logship s3: client for %s: %w", cfg.Endpoint, err)
	}
	prefix := cfg.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &S3Sink{client: client, bucket: cfg.Bucket, prefix: prefix}, nil
}

func (s *S3Sink) key(name string) string { return s.prefix + name }

func (s *S3Sink) Put(ctx context.Context, name string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.key(name), bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("logship s3: put %s: %w", name, err)
	}
	return nil
}

func (s *S3Sink) Get(ctx context.Context, name string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(name), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("logship s3: get %s: %w", name, err)
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("logship s3: read %s: %w", name, err)
	}
	return data, nil
}

// List returns the segment names under prefix, sink-relative (the configured key
// prefix is stripped), in lexical order — the same names Put took.
func (s *S3Sink) List(ctx context.Context, prefix string) ([]string, error) {
	var names []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    s.prefix + prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("logship s3: listing %q: %w", prefix, obj.Err)
		}
		names = append(names, strings.TrimPrefix(obj.Key, s.prefix))
	}
	sort.Strings(names)
	return names, nil
}
