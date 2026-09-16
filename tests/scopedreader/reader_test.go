//go:build integration

package scopedreader

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

type Reader interface {
	Open(context.Context, string, evidence.Ref) (io.ReadCloser, error)
	Close() error
}

var Production struct {
	Open func(context.Context, evidence.Config) (Reader, error)
}

func requireFactory(t *testing.T) {
	t.Helper()
	if Production.Open == nil {
		t.Fatal("production scoped-reader binding missing; do not grant whole-bucket permission to satisfy startup probes")
	}
}

func TestScopedReaderUsesRealNarrowCredentialWithoutBucketProbe(t *testing.T) {
	requireFactory(t)
	path := os.Getenv("ASPM_SCOPED_READER_CONFIG")
	if path == "" {
		t.Fatal("explicit private storage fixture configuration is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("private fixture configuration could not be read")
	}
	var config struct {
		Endpoint, Region, Bucket string
		Roles                    map[string]struct{ AccessKey, SecretKey string }
	}
	if json.Unmarshal(raw, &config) != nil || config.Endpoint != "http://127.0.0.1:18335" || config.Bucket != "aspm-isolation" {
		t.Fatal("fixture is not the explicitly owned security store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	newClient := func(role string) *s3.Client {
		c := config.Roles[role]
		return s3.New(s3.Options{
			Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
			Credentials:      credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
			RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		})
	}
	admin, ai := newClient("admin"), newClient("ai")
	_, err = ai.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(config.Bucket)})
	var status interface{ HTTPStatusCode() int }
	if !errors.As(err, &status) || status.HTTPStatusCode() != 403 {
		t.Fatal("AI fixture must actually lack whole-bucket probe permission")
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal("fixture identity generation failed")
	}
	suffix := "reader-" + hex.EncodeToString(id[:])
	data := []byte("explicitly synthetic scoped reader\r\n\x00")
	ref := evidence.Ref{WorkspaceID: "team-a", Bucket: config.Bucket,
		Key: "approved/team-a/grant-1/" + suffix, SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), SizeBytes: int64(len(data))}
	if _, err := admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key), Body: bytes.NewReader(data), ContentLength: aws.Int64(ref.SizeBytes)}); err != nil {
		t.Fatal("admin could not seed the owned scoped-reader object")
	}
	defer func() {
		if _, err := admin.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)}); err != nil {
			t.Error("owned reader fixture cleanup failed")
		}
	}()
	key := config.Roles["ai"]
	reader, err := Production.Open(ctx, evidence.Config{Endpoint: config.Endpoint, AccessKey: key.AccessKey, SecretKey: key.SecretKey,
		Bucket: config.Bucket, Prefix: "approved/", Region: config.Region, Timeout: 3 * time.Second})
	if err != nil || reader == nil {
		t.Fatal("a valid narrow read credential could not construct a reader without broad probing")
	}
	defer reader.Close()
	if _, writable := reader.(interface {
		Put(context.Context, string, string, io.Reader, int64) (evidence.Ref, error)
	}); writable {
		t.Fatal("read-only worker received a write-capable storage interface")
	}
	body, err := reader.Open(ctx, "team-a", ref)
	if err != nil {
		t.Fatal("approved scoped evidence could not be opened")
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, data) {
		t.Fatal("approved scoped evidence did not retain exact bytes and integrity")
	}
	if _, err := reader.Open(ctx, "team-b", ref); !errors.Is(err, evidence.ErrScope) {
		t.Fatal("reader widened caller workspace scope")
	}
}

func TestScopedReaderNeverFallsBackToAmbientCredentials(t *testing.T) {
	requireFactory(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "synthetic-ambient-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "synthetic-ambient-secret")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer server.Close()
	for _, keys := range [][2]string{{"", ""}, {"explicit-key", ""}, {"", "explicit-secret"}} {
		reader, err := Production.Open(context.Background(), evidence.Config{Endpoint: server.URL,
			AccessKey: keys[0], SecretKey: keys[1], Bucket: "fixture", Prefix: "approved/", Region: "us-east-1", Timeout: time.Second})
		if err == nil || reader != nil {
			t.Fatal("missing explicit read identity returned a usable reader")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("missing credentials caused a probe, unsigned request or ambient credential use")
	}
}
