//go:build integration

package storage_security

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("required private fixture variable %s is missing; no live gate was run", name)
	}
	return value
}

func must(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; credential/response details withheld)", operation, err)
	}
}

func clients(t *testing.T) (map[string]*s3.Client, string) {
	t.Helper()
	endpoint := required(t, "ASPM_STORAGE_TEST_ENDPOINT")
	parsed, err := url.Parse(endpoint)
	must(t, "parse owned fixture endpoint", err)
	if parsed.Scheme != "http" || parsed.Host != "127.0.0.1:18335" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		t.Fatal("storage security tests require the explicitly owned loopback 18335 endpoint")
	}
	bucket := required(t, "ASPM_STORAGE_TEST_BUCKET")
	if bucket != "aspm-isolation" {
		t.Fatal("storage security tests require the owned aspm-isolation bucket")
	}
	region := required(t, "ASPM_STORAGE_TEST_REGION")
	result := make(map[string]*s3.Client)
	identities := make(map[string]bool)
	for _, role := range []string{"admin", "ai", "ingestion", "core"} {
		prefix := "ASPM_STORAGE_TEST_" + strings.ToUpper(role)
		key, secret := required(t, prefix+"_ACCESS_KEY"), required(t, prefix+"_SECRET_KEY")
		if identities[key] {
			t.Fatal("fixture roles must have distinct credential identities; values withheld")
		}
		identities[key] = true
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
		client := &http.Client{
			Transport: transport, Timeout: 4 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("storage fixture redirects are forbidden")
			},
		}
		result[role] = s3.New(s3.Options{
			Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
			Credentials: credentials.NewStaticCredentialsProvider(key, secret, ""),
			HTTPClient:  client, RetryMaxAttempts: 1,
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		})
	}
	return result, bucket
}

func put(ctx context.Context, client *s3.Client, bucket, key string, data []byte) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))), ContentType: aws.String("application/octet-stream"),
	})
	return err
}

func get(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	result, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Body == nil {
		return nil, errors.New("S3 returned no object body")
	}
	data, readErr := io.ReadAll(io.LimitReader(result.Body, 4097))
	closeErr := result.Body.Close()
	if len(data) > 4096 {
		return nil, errors.Join(errors.New("owned probe exceeded its byte bound"), readErr, closeErr)
	}
	return data, errors.Join(readErr, closeErr)
}

func denied(t *testing.T, operation string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: actual role-signed S3 operation succeeded; require HTTP 403 / AccessDenied", operation)
		return
	}
	var status interface{ HTTPStatusCode() int }
	var apiError smithy.APIError
	httpStatus := 0
	if errors.As(err, &status) {
		httpStatus = status.HTTPStatusCode()
	}
	accessDenied := errors.As(err, &apiError) && apiError.ErrorCode() == "AccessDenied"
	if httpStatus != http.StatusForbidden || !accessDenied {
		t.Errorf("%s: require HTTP 403 / AccessDenied; status=%d accessDenied=%t errorType=%T",
			operation, httpStatus, accessDenied, err)
	}
}

func TestStorageRoleCredentials(t *testing.T) {
	stores, bucket := clients(t)
	paths := []struct{ name, prefix string }{
		{"raw-team-a", "raw/team-a/"}, {"raw-team-b", "raw/team-b/"},
		{"approved-grant-1", "approved/team-a/grant-1/"}, {"approved-grant-2", "approved/team-a/grant-2/"},
		{"approved-team-b", "approved/team-b/"}, {"normalized-team-a", "normalized/team-a/"},
		{"normalized-team-b", "normalized/team-b/"}, {"other", "other/"},
	}
	for _, role := range []string{"ai", "ingestion", "core"} {
		t.Run(role, func(t *testing.T) {
			for _, path := range paths {
				t.Run(path.name, func(t *testing.T) {
					nonce := make([]byte, 12)
					_, err := rand.Read(nonce)
					must(t, "create owned probe suffix", err)
					key := path.prefix + "security-" + hex.EncodeToString(nonce) + ".bin"
					original := []byte("synthetic storage authorization fixture\n")
					changed := []byte("synthetic role write verification\n")
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					t.Cleanup(cancel)
					t.Cleanup(func() {
						cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
						defer stop()
						_, cleanupErr := stores["admin"].DeleteObject(cleanup, &s3.DeleteObjectInput{
							Bucket: aws.String(bucket), Key: aws.String(key),
						})
						if cleanupErr != nil {
							t.Errorf("admin cleanup of owned probe failed (%T; details withheld)", cleanupErr)
						}
					})
					must(t, "admin preseed owned object", put(ctx, stores["admin"], bucket, key, original))
					seeded, err := get(ctx, stores["admin"], bucket, key)
					must(t, "admin verify seeded object exists", err)
					if !bytes.Equal(seeded, original) {
						t.Fatal("admin could not verify exact seeded bytes; denial would not be meaningful")
					}
					coreScope := strings.HasPrefix(path.prefix, "raw/") || strings.HasPrefix(path.prefix, "approved/")
					readAllowed := role == "core" && coreScope ||
						role == "ai" && path.prefix == "approved/team-a/grant-1/" ||
						role == "ingestion" && path.prefix == "raw/team-a/"
					writeAllowed := role == "core" && coreScope ||
						role == "ingestion" && path.prefix == "normalized/team-a/"
					t.Run("read", func(t *testing.T) {
						data, readErr := get(ctx, stores[role], bucket, key)
						if !readAllowed {
							denied(t, "GetObject", readErr)
						} else {
							must(t, "permitted role GetObject", readErr)
							if !bytes.Equal(data, original) {
								t.Error("permitted role did not read the exact admin-seeded object")
							}
						}
					})
					t.Run("write", func(t *testing.T) {
						writeErr := put(ctx, stores[role], bucket, key, changed)
						expected := original
						if !writeAllowed {
							denied(t, "PutObject", writeErr)
						} else {
							must(t, "permitted role PutObject", writeErr)
							expected = changed
						}
						persisted, verifyErr := get(ctx, stores["admin"], bucket, key)
						must(t, "admin verify object after role write", verifyErr)
						if !bytes.Equal(persisted, expected) {
							t.Error("actual object bytes contradict the required role write permission")
						}
					})
				})
			}
		})
	}
}
