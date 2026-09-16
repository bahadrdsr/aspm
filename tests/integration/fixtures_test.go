//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

type fixture struct {
	ctx       context.Context
	db        *pgxpool.Pool
	s3        *s3.Client
	jobs      JobConfig
	evidence  EvidenceConfig
	workspace string
}

func requireOK(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; sensitive error details withheld)", operation, err)
	}
}

func requireError(t *testing.T, operation string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s: expected %s, received %T (details withheld)", operation, want, got)
	}
}

func nonce(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	_, err := rand.Read(value)
	requireOK(t, "generate isolated test namespace", err)
	return hex.EncodeToString(value)
}

func identifier(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	requireOK(t, "generate synthetic identity", err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

func digest(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func environment() (JobConfig, EvidenceConfig, error) {
	names := []string{
		"ASPM_TEST_DATABASE_URL", "ASPM_TEST_S3_ENDPOINT", "ASPM_TEST_S3_ACCESS_KEY",
		"ASPM_TEST_S3_SECRET_KEY", "ASPM_TEST_S3_BUCKET",
	}
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return JobConfig{}, EvidenceConfig{}, fmt.Errorf("required fixture variable %s is missing", name)
		}
	}
	databaseURL := os.Getenv(names[0])
	database, err := url.Parse(databaseURL)
	if err != nil || (database.Scheme != "postgres" && database.Scheme != "postgresql") ||
		database.Host == "" || database.Path == "" || database.Path == "/" {
		return JobConfig{}, EvidenceConfig{}, errors.New("invalid PostgreSQL fixture URL; value withheld")
	}
	if database.User == nil || database.User.Username() == "" {
		return JobConfig{}, EvidenceConfig{}, errors.New("PostgreSQL fixture URL must specify credentials, not ambient profile defaults")
	}
	if password, present := database.User.Password(); !present || password == "" {
		return JobConfig{}, EvidenceConfig{}, errors.New("PostgreSQL fixture URL must specify a protected nonempty password")
	}
	endpoint, err := url.Parse(os.Getenv(names[1]))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return JobConfig{}, EvidenceConfig{}, errors.New("invalid S3 fixture endpoint; value withheld")
	}
	bucket := os.Getenv(names[4])
	if strings.ContainsAny(bucket, "/\\:") {
		return JobConfig{}, EvidenceConfig{}, errors.New("fixture bucket must be a plain bucket name, not an ARN or URL")
	}
	return JobConfig{
			DatabaseURL: databaseURL, MaxConnections: 2, MaxLease: 2 * time.Second,
			MaxAttempts: 3, RetryDelay: time.Second, MaxRetryDelay: 2 * time.Second,
		}, EvidenceConfig{
			Endpoint: endpoint.String(), AccessKey: os.Getenv(names[2]), SecretKey: os.Getenv(names[3]),
			Bucket: bucket, Region: "us-east-1", Timeout: 5 * time.Second,
		}, nil
}

func rawS3(config EvidenceConfig) *s3.Client {
	client := &http.Client{
		Timeout: config.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("fixture requests must not follow redirects")
		},
	}
	return s3.New(s3.Options{
		Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient:  client, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	jobs, evidence, err := environment()
	if err != nil {
		t.Fatalf("M02 fixture configuration unavailable: %s", err)
	}
	id := nonce(t)
	jobs.Schema = "m02_it_" + id
	jobs.ApplicationName = jobs.Schema
	evidence.Prefix = "m02-it/" + id + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	poolConfig, err := pgxpool.ParseConfig(jobs.DatabaseURL)
	requireOK(t, "parse PostgreSQL fixture configuration", err)
	poolConfig.MaxConns = 2
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	poolConfig.ConnConfig.RuntimeParams["application_name"] = jobs.ApplicationName + "-fixture"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	requireOK(t, "open PostgreSQL fixture pool", err)
	t.Cleanup(pool.Close)
	requireOK(t, "authenticate to PostgreSQL fixture", pool.Ping(ctx))
	storage := rawS3(evidence)
	_, err = storage.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(evidence.Bucket)})
	requireOK(t, "authenticate to the existing S3 fixture bucket", err)
	f := &fixture{ctx: ctx, db: pool, s3: storage, jobs: jobs, evidence: evidence, workspace: identifier(t)}
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{jobs.Schema}.Sanitize())
	requireOK(t, "create owned PostgreSQL schema", err)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if !regexp.MustCompile(`^m02_it_[a-f0-9]{24}$`).MatchString(f.jobs.Schema) ||
			f.evidence.Prefix != "m02-it/"+strings.TrimPrefix(f.jobs.Schema, "m02_it_")+"/" {
			t.Error("refusing cleanup of a namespace not created by this fixture")
			return
		}
		pages := s3.NewListObjectsV2Paginator(f.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(f.evidence.Bucket), Prefix: aws.String(f.evidence.Prefix), MaxKeys: aws.Int32(100),
		})
		for page := 0; pages.HasMorePages(); page++ {
			if page >= 10 {
				t.Error("owned-object cleanup exceeded its bounded page allowance")
				break
			}
			output, listErr := pages.NextPage(cleanupCtx)
			if listErr != nil {
				t.Errorf("list owned cleanup objects failed (%T; details withheld)", listErr)
				break
			}
			for _, object := range output.Contents {
				key := aws.ToString(object.Key)
				if !strings.HasPrefix(key, f.evidence.Prefix) {
					t.Error("refusing to delete an object outside the owned prefix")
					continue
				}
				if _, deleteErr := f.s3.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{
					Bucket: aws.String(f.evidence.Bucket), Key: aws.String(key),
				}); deleteErr != nil {
					t.Errorf("delete owned test object failed (%T; details withheld)", deleteErr)
				}
			}
		}
		if _, dropErr := f.db.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{f.jobs.Schema}.Sanitize()+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned test schema failed (%T; details withheld)", dropErr)
		}
	})
	return f
}

func (f *fixture) openJobs(t *testing.T, suffix string) Jobs {
	t.Helper()
	config := f.jobs
	config.ApplicationName += suffix
	store, err := Production.OpenJobs(f.ctx, config)
	requireOK(t, "open production durable jobs", err)
	if store == nil {
		t.Fatal("production OpenJobs returned a nil store without an error")
	}
	t.Cleanup(func() { requireOK(t, "close production jobs", store.Close()) })
	return store
}

func (f *fixture) openEvidence(t *testing.T) Evidence {
	t.Helper()
	store, err := Production.OpenEvidence(f.ctx, f.evidence)
	requireOK(t, "open production shared evidence client", err)
	if store == nil {
		t.Fatal("production OpenEvidence returned a nil client without an error")
	}
	t.Cleanup(func() { requireOK(t, "close production evidence client", store.Close()) })
	return store
}

func (f *fixture) rawPut(t *testing.T, workspace, objectID string, data []byte) EvidenceRef {
	t.Helper()
	key := f.evidence.Prefix + workspace + "/" + objectID
	_, err := f.s3.PutObject(f.ctx, &s3.PutObjectInput{
		Bucket: aws.String(f.evidence.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		ContentType: aws.String("application/octet-stream"),
	})
	requireOK(t, "write owned synthetic S3 object", err)
	return EvidenceRef{WorkspaceID: workspace, Bucket: f.evidence.Bucket, Key: key, SHA256: digest(data), SizeBytes: int64(len(data))}
}

func (f *fixture) envelope(t *testing.T) Envelope {
	t.Helper()
	return Envelope{
		Version: EnvelopeVersion, Kind: IntegrityKind, WorkspaceID: f.workspace,
		SourceID: identifier(t), RunID: identifier(t), BatchID: identifier(t), TraceID: identifier(t),
		ParserRevision: "m02-integrity-v1-not-a-scanner-parser", IdempotencyKey: identifier(t), MaxAttempts: 2,
		Evidence: f.rawPut(t, f.workspace, identifier(t), []byte("synthetic M02 evidence\r\n\x00\xff")),
	}
}

func (f *fixture) now(t *testing.T) time.Time {
	t.Helper()
	var result time.Time
	requireOK(t, "read PostgreSQL lease clock", f.db.QueryRow(f.ctx, "SELECT clock_timestamp()").Scan(&result))
	return result
}

func (f *fixture) after(t *testing.T, timestamp time.Time) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !f.now(t).After(timestamp) {
		if time.Now().After(deadline) {
			t.Fatal("bounded wait for PostgreSQL lease/retry eligibility expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readEvidence(ctx context.Context, store Evidence, workspace string, ref EvidenceRef) ([]byte, error) {
	reader, err := store.Open(ctx, workspace, ref)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("nil evidence reader")
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, 1<<20))
	closeErr := reader.Close()
	return data, errors.Join(readErr, closeErr)
}

func TestM02LiveFixtureRoundTrips(t *testing.T) {
	f := newFixture(t)
	t.Run("postgres-authenticated-commit-and-reconnect", func(t *testing.T) {
		table := pgx.Identifier{f.jobs.Schema, "fixture_probe"}.Sanitize()
		tx, err := f.db.Begin(f.ctx)
		requireOK(t, "begin owned PostgreSQL fixture transaction", err)
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				t.Errorf("close probe transaction failed (%T; details withheld)", rollbackErr)
			}
		}()
		_, err = tx.Exec(f.ctx, "CREATE TABLE "+table+" (id integer PRIMARY KEY, marker text NOT NULL)")
		requireOK(t, "create owned probe table", err)
		_, err = tx.Exec(f.ctx, "INSERT INTO "+table+" VALUES (1, 'synthetic-m02')")
		requireOK(t, "insert synthetic PostgreSQL probe", err)
		requireOK(t, "commit synthetic PostgreSQL probe", tx.Commit(f.ctx))
		connection, err := pgx.Connect(f.ctx, f.jobs.DatabaseURL)
		requireOK(t, "open independent PostgreSQL connection", err)
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if closeErr := connection.Close(ctx); closeErr != nil {
				t.Errorf("close independent probe connection failed (%T; details withheld)", closeErr)
			}
		}()
		var marker, version string
		requireOK(t, "read committed probe through a new connection", connection.QueryRow(f.ctx, "SELECT marker FROM "+table+" WHERE id=1").Scan(&marker))
		if marker != "synthetic-m02" {
			t.Fatal("committed fixture data was not durable across connections")
		}
		requireOK(t, "read PostgreSQL server version", connection.QueryRow(f.ctx, "SHOW server_version").Scan(&version))
		t.Logf("authenticated PostgreSQL commit/reconnect passed; server version %s", version)
	})
	t.Run("s3-authenticated-exact-byte-roundtrip", func(t *testing.T) {
		payload := []byte("synthetic S3 fixture\r\n\x00\xff")
		ref := f.rawPut(t, f.workspace, "fixture-probe", payload)
		output, err := rawS3(f.evidence).GetObject(f.ctx, &s3.GetObjectInput{
			Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key),
		})
		requireOK(t, "read owned S3 object through a fresh SDK client", err)
		defer output.Body.Close()
		data, err := io.ReadAll(output.Body)
		requireOK(t, "read S3 probe body", err)
		requireOK(t, "close S3 probe body", output.Body.Close())
		if !bytes.Equal(data, payload) || digest(data) != ref.SHA256 {
			t.Fatal("S3 fixture did not preserve exact synthetic bytes")
		}
		t.Log("authenticated shared S3 exact-byte/digest roundtrip passed")
	})
}
