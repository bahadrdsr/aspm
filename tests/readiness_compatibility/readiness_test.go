//go:build integration

package readiness_compatibility

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

type requestRecord struct {
	method, key, conditional, code string
	status                         int
}

type readinessFixture struct {
	ctx          context.Context
	config       evidence.Config
	admin, role  *s3.Client
	probe, check string
	mu           sync.Mutex
	calls        []requestRecord
}

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("missing private fixture variable %s; no compatibility gate was run", name)
	}
	return value
}

func must(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; credentials/response details withheld)", operation, err)
	}
}

func client(t *testing.T, config evidence.Config) *s3.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	return s3.New(s3.Options{
		Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: 4 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects denied") }},
		RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func put(ctx context.Context, client *s3.Client, bucket, key string, data []byte) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data)))})
	return err
}

func read(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	output, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	if output == nil || output.Body == nil {
		return nil, errors.New("missing real S3 body")
	}
	data, readErr := io.ReadAll(io.LimitReader(output.Body, 4097))
	closeErr := output.Body.Close()
	if len(data) > 4096 {
		return nil, errors.Join(errors.New("probe byte bound exceeded"), readErr, closeErr)
	}
	return data, errors.Join(readErr, closeErr)
}

func nativeDenied(err error) bool {
	var response interface{ HTTPStatusCode() int }
	var api smithy.APIError
	return errors.As(err, &response) && response.HTTPStatusCode() == 403 &&
		errors.As(err, &api) && api.ErrorCode() == "AccessDenied"
}

func fixture(t *testing.T, role, prefix string) *readinessFixture {
	t.Helper()
	endpoint := required(t, "ASPM_READINESS_COMPAT_ENDPOINT")
	bucket := required(t, "ASPM_READINESS_COMPAT_BUCKET")
	if endpoint != "http://127.0.0.1:18335" || bucket != "aspm-isolation" {
		t.Fatal("only the explicitly owned security-store endpoint/bucket is permitted")
	}
	region := required(t, "ASPM_READINESS_COMPAT_REGION")
	identities := make(map[string]evidence.Config)
	seen := make(map[string]bool)
	for _, name := range []string{"admin", "core", "ingestion"} {
		key := required(t, "ASPM_READINESS_COMPAT_"+strings.ToUpper(name)+"_ACCESS_KEY")
		if seen[key] {
			t.Fatal("admin/core/ingestion identities must be distinct; values withheld")
		}
		seen[key] = true
		identities[name] = evidence.Config{Endpoint: endpoint, Bucket: bucket, Region: region, AccessKey: key,
			SecretKey: required(t, "ASPM_READINESS_COMPAT_"+strings.ToUpper(name)+"_SECRET_KEY"), Timeout: 4 * time.Second}
	}
	nonce := make([]byte, 12)
	_, err := rand.Read(nonce)
	must(t, "generate owned target suffix", err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	f := &readinessFixture{ctx: ctx, config: identities[role], admin: client(t, identities["admin"])}
	f.config.Prefix = prefix + "readiness-compat-" + hex.EncodeToString(nonce) + "/"
	f.probe, f.check = f.config.Prefix+"probe.txt", f.config.Prefix+"role-control.txt"
	target, err := url.Parse(endpoint)
	must(t, "parse explicit storage endpoint", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport, proxy.ErrorLog = transport, log.New(io.Discard, "", 0)
	t.Cleanup(transport.CloseIdleConnections)
	proxy.ModifyResponse = func(response *http.Response) error {
		record := requestRecord{method: response.Request.Method,
			key:         strings.TrimPrefix(response.Request.URL.Path, "/"+bucket+"/"),
			conditional: response.Request.Header.Get("If-None-Match"), status: response.StatusCode}
		if response.StatusCode >= 400 {
			body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
			closeErr := response.Body.Close()
			if err != nil || closeErr != nil || len(body) > 65536 {
				t.Error("could not observe bounded native error response")
				return errors.New("native error observation failed")
			}
			var native struct {
				Code string `xml:"Code"`
			}
			if xml.Unmarshal(body, &native) == nil {
				record.code = native.Code
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
		}
		f.mu.Lock()
		f.calls = append(f.calls, record)
		f.mu.Unlock()
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, credential, found := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		key, _, _ := strings.Cut(credential, "/")
		if !found || key != identities[role].AccessKey {
			t.Error("actual signing identity differs from selected role; values withheld")
			http.Error(w, "unexpected test signing identity", 403)
			return
		}
		object := strings.TrimPrefix(r.URL.Path, "/"+bucket+"/")
		root := r.URL.Path == "/"+bucket || r.URL.Path == "/"+bucket+"/"
		if root || r.Method != "GET" && r.Method != "PUT" || object != f.probe && object != f.check {
			t.Error("production requested HeadBucket/list or an operation outside owned readiness targets")
			http.Error(w, "unexpected readiness operation", 403)
			return
		}
		// Preserve signed Host/path/body and the actual service response; never synthesize success.
		proxy.ServeHTTP(w, r)
	}))
	f.config.Endpoint = server.URL
	t.Cleanup(server.Close)
	f.role = client(t, f.config)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		for _, key := range []string{f.probe, f.check} {
			_, err := f.admin.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				t.Errorf("admin owned-object cleanup failed (%T; details withheld)", err)
			}
		}
	})
	return f
}

func (f *readinessFixture) probeCalls() []requestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var selected []requestRecord
	for _, call := range f.calls {
		if call.key == f.probe {
			selected = append(selected, call)
		}
	}
	return selected
}

func TestReadinessConditionalFirstCompatibility(t *testing.T) {
	for _, name := range []string{"core-fresh", "ingestion-raw-read-only", "ingestion-normalized-write-only", "core-existing-sentinel"} {
		t.Run(name, func(t *testing.T) {
			role, prefix := "core", "raw/team-a/"
			if strings.HasPrefix(name, "ingestion") {
				role = "ingestion"
			}
			if name == "ingestion-normalized-write-only" {
				prefix = "normalized/team-a/"
			}
			f := fixture(t, role, prefix)
			sentinel := []byte("synthetic readiness sentinel; never overwrite\r\n")
			if name == "core-existing-sentinel" {
				must(t, "admin preseed exact sentinel", put(f.ctx, f.admin, f.config.Bucket, f.probe, sentinel))
				data, err := read(f.ctx, f.admin, f.config.Bucket, f.probe)
				must(t, "admin verify existing sentinel", err)
				if !bytes.Equal(data, sentinel) {
					t.Fatal("sentinel precondition not established")
				}
			}
			if name == "ingestion-raw-read-only" {
				must(t, "admin seed read-only role control", put(f.ctx, f.admin, f.config.Bucket, f.check, sentinel))
				data, err := read(f.ctx, f.role, f.config.Bucket, f.check)
				must(t, "actual ingestion raw GetObject capability", err)
				if !bytes.Equal(data, sentinel) {
					t.Fatal("read-only fixture control did not preserve seeded bytes")
				}
				if err := put(f.ctx, f.role, f.config.Bucket, f.check, []byte("synthetic denied write\n")); !nativeDenied(err) {
					t.Fatal("raw read-only identity must receive native PUT 403/AccessDenied")
				}
				data, err = read(f.ctx, f.admin, f.config.Bucket, f.check)
				must(t, "admin verify denied role write did not mutate control", err)
				if !bytes.Equal(data, sentinel) {
					t.Fatal("denied role write changed actual control bytes")
				}
			}
			if name == "ingestion-normalized-write-only" {
				must(t, "actual ingestion normalized PutObject capability", put(f.ctx, f.role, f.config.Bucket, f.check, sentinel))
				data, err := read(f.ctx, f.admin, f.config.Bucket, f.check)
				must(t, "admin verify real role-written control", err)
				if !bytes.Equal(data, sentinel) {
					t.Fatal("write-only fixture control did not persist")
				}
				_, err = read(f.ctx, f.role, f.config.Bucket, f.check)
				if !nativeDenied(err) {
					t.Fatal("existing normalized policy must provide real PUT success and GET 403/AccessDenied; not a skip")
				}
			}
			result := evidence.PrepareReadiness(f.ctx, f.config, f.probe)
			calls := f.probeCalls()
			if len(calls) == 0 {
				t.Fatal("PrepareReadiness performed no actual selected-object request")
			}
			var trace []string
			for _, call := range calls {
				trace = append(trace, fmt.Sprintf("%s:%d", call.method, call.status))
			}
			t.Logf("actual selected-object native sequence: %s", strings.Join(trace, " -> "))
			t.Run("conditional-first-and-confirmation", func(t *testing.T) {
				if calls[0].method != "PUT" || calls[0].conditional != "*" {
					t.Errorf("first actual request is %s, require PUT with If-None-Match:*", calls[0].method)
				}
				wantPut, wantGet := 200, 200
				if name == "core-existing-sentinel" {
					wantPut = 412
				} else if name == "ingestion-raw-read-only" {
					wantPut, wantGet = 403, 0
				} else if name == "ingestion-normalized-write-only" {
					wantGet = 403
				}
				sawPut, confirmed := false, false
				for _, call := range calls {
					if call.method == "PUT" {
						if call.conditional != "*" || call.status != wantPut {
							t.Errorf("conditional PUT status=%d, want=%d with If-None-Match:*", call.status, wantPut)
						}
						if wantPut == 412 && call.code != "PreconditionFailed" {
							t.Error("existing object must produce a native conditional conflict")
						}
						sawPut = true
					} else if sawPut && call.method == "GET" {
						if wantGet == 0 || call.status != wantGet {
							t.Errorf("readback status=%d, want=%d (0 means no GET after denied PUT)", call.status, wantGet)
						}
						confirmed = true
					}
				}
				if !sawPut || wantGet != 0 && !confirmed {
					t.Error("missing actual conditional PUT or mandatory subsequent readable-object confirmation")
				}
			})
			t.Run("native-denial-or-byte-preservation", func(t *testing.T) {
				if role == "core" {
					must(t, "actual core readiness preparation", result)
					data, err := read(f.ctx, f.admin, f.config.Bucket, f.probe)
					must(t, "admin confirm resulting real object", err)
					if len(data) == 0 || name == "core-existing-sentinel" && !bytes.Equal(data, sentinel) {
						t.Error("readiness lost readable bytes or overwrote the existing sentinel")
					}
				} else {
					denied := false
					for _, call := range calls {
						if call.status == 403 && call.code == "AccessDenied" {
							denied = true
						}
					}
					if result == nil || !denied {
						t.Error("actual role 403/AccessDenied must remain failure, not readiness success")
					}
				}
			})
		})
	}
}
