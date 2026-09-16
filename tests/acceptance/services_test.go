//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type services struct {
	ctx context.Context
	cfg ApplicationConfig
	db  *pgxpool.Pool
	s3  *s3.Client
}

func digest(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

func ownedServices(t *testing.T) *services {
	t.Helper()
	names := []string{"ASPM_TEST_DATABASE_URL", "ASPM_TEST_S3_ENDPOINT", "ASPM_TEST_S3_ACCESS_KEY", "ASPM_TEST_S3_SECRET_KEY", "ASPM_TEST_S3_BUCKET"}
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			t.Fatalf("BLOCKED: required process-local %s is missing; services are not skipped", name)
		}
	}
	dbURL, err := url.Parse(os.Getenv(names[0]))
	if err != nil || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") || dbURL.Host == "" || dbURL.Path == "" || dbURL.Path == "/" || dbURL.User == nil || dbURL.User.Username() == "" {
		t.Fatal("BLOCKED: invalid explicit PostgreSQL fixture URL; value withheld")
	}
	if password, present := dbURL.User.Password(); !present || password == "" {
		t.Fatal("BLOCKED: explicit PostgreSQL fixture credentials are required")
	}
	endpoint, err := url.Parse(os.Getenv(names[1]))
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		t.Fatal("BLOCKED: invalid S3 fixture endpoint; value withheld")
	}
	bucket := os.Getenv(names[4])
	if strings.ContainsAny(bucket, "/\\:") {
		t.Fatal("BLOCKED: S3 bucket must be a plain existing bucket name")
	}
	id := nonce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &services{ctx: ctx, cfg: ApplicationConfig{
		DatabaseURL: os.Getenv(names[0]), Schema: "acceptance_" + id, ApplicationName: "acceptance_" + id,
		MaxConnections: 4, MaxUploadBytes: 64 << 10, SessionTTL: 30 * time.Minute, ManualProcessing: true,
		Storage: StorageConfig{Endpoint: endpoint.String(), Bucket: bucket, Prefix: "acceptance/" + id + "/",
			AccessKey: os.Getenv(names[2]), SecretKey: os.Getenv(names[3]), Region: "us-east-1"},
	}}
	pg, err := pgxpool.ParseConfig(f.cfg.DatabaseURL)
	ok(t, "parse fixture connection configuration", err)
	pg.MaxConns, pg.ConnConfig.ConnectTimeout = 1, 5*time.Second
	pg.ConnConfig.RuntimeParams["application_name"] = f.cfg.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pg)
	ok(t, "open fixture PostgreSQL", err)
	t.Cleanup(f.db.Close)
	ok(t, "BLOCKED unless PostgreSQL authenticates", f.db.Ping(ctx))
	st := f.cfg.Storage
	f.s3 = s3.New(s3.Options{
		Region: st.Region, BaseEndpoint: aws.String(st.Endpoint), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider(st.AccessKey, st.SecretKey, ""),
		HTTPClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("fixture redirects are disabled")
		}},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	_, err = f.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(st.Bucket)})
	ok(t, "BLOCKED unless the existing S3 bucket authenticates", err)
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.cfg.Schema}.Sanitize())
	ok(t, "create owned fixture schema", err)
	t.Cleanup(func() { f.cleanup(t) })
	return f
}

func (f *services) cleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !regexp.MustCompile(`^acceptance_[a-f0-9]{24}$`).MatchString(f.cfg.Schema) ||
		f.cfg.Storage.Prefix != "acceptance/"+strings.TrimPrefix(f.cfg.Schema, "acceptance_")+"/" {
		t.Error("refusing cleanup of a namespace not owned by this fixture")
		return
	}
	pages := s3.NewListObjectsV2Paginator(f.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(f.cfg.Storage.Bucket), Prefix: aws.String(f.cfg.Storage.Prefix), MaxKeys: aws.Int32(100),
	})
	for n := 0; pages.HasMorePages(); n++ {
		if n == 4 {
			t.Error("owned S3 cleanup exceeded four pages")
			break
		}
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Errorf("owned S3 cleanup listing failed (%T; details withheld)", err)
			break
		}
		for _, item := range page.Contents {
			if !strings.HasPrefix(aws.ToString(item.Key), f.cfg.Storage.Prefix) {
				t.Error("refusing S3 deletion outside the owned prefix")
				continue
			}
			_, err := f.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(f.cfg.Storage.Bucket), Key: item.Key})
			if err != nil {
				t.Errorf("owned S3 cleanup failed (%T; details withheld)", err)
			}
		}
	}
	if _, err := f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.cfg.Schema}.Sanitize()+" CASCADE"); err != nil {
		t.Errorf("owned schema cleanup failed (%T; details withheld)", err)
	}
}

func (f *services) storedReport(t *testing.T, workspace string, want []byte) {
	t.Helper()
	page, err := f.s3.ListObjectsV2(f.ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(f.cfg.Storage.Bucket), Prefix: aws.String(f.cfg.Storage.Prefix + workspace + "/"), MaxKeys: aws.Int32(100),
	})
	ok(t, "read owned production evidence listing", err)
	if aws.ToBool(page.IsTruncated) {
		t.Fatal("small acceptance import exceeded the bounded evidence listing")
	}
	for _, item := range page.Contents {
		if aws.ToInt64(item.Size) != int64(len(want)) {
			continue
		}
		result, err := f.s3.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(f.cfg.Storage.Bucket), Key: item.Key})
		ok(t, "independently read production S3 evidence", err)
		data, err := io.ReadAll(io.LimitReader(result.Body, int64(len(want))+1))
		closeErr := result.Body.Close()
		ok(t, "read bounded evidence bytes", err)
		ok(t, "close evidence reader", closeErr)
		if bytes.Equal(data, want) {
			return
		}
	}
	t.Fatal("exact original report was not present in the real workspace-scoped S3 prefix")
}

func TestAcceptance_OwnedServices(t *testing.T) {
	f := ownedServices(t)
	table := pgx.Identifier{f.cfg.Schema, "fixture_probe"}.Sanitize()
	_, err := f.db.Exec(f.ctx, "CREATE TABLE "+table+" (value text NOT NULL)")
	ok(t, "create owned fixture probe", err)
	_, err = f.db.Exec(f.ctx, "INSERT INTO "+table+" (value) VALUES ($1)", "synthetic committed probe")
	ok(t, "commit fixture probe", err)
	conn, err := pgx.Connect(f.ctx, f.cfg.DatabaseURL)
	ok(t, "open independent PostgreSQL connection", err)
	defer conn.Close(f.ctx)
	var value string
	ok(t, "read committed probe independently", conn.QueryRow(f.ctx, "SELECT value FROM "+table).Scan(&value))
	equal(t, "committed probe", value, "synthetic committed probe")
	data := []byte("synthetic shared acceptance evidence\r\n")
	_, err = f.s3.PutObject(f.ctx, &s3.PutObjectInput{
		Bucket: aws.String(f.cfg.Storage.Bucket), Key: aws.String(f.cfg.Storage.Prefix + "probe/report"),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
	})
	ok(t, "write owned S3 probe", err)
	f.storedReport(t, "probe", data)
}
