package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

type reportStore struct {
	client    *s3.Client
	transport *http.Transport
	config    StorageConfig
	limit     int64
	reader    *evidence.Reader
}

var storageSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

func ValidateStorageConfig(config StorageConfig) error {
	_, err := normalizedStorageConfig(config)
	return err
}

func normalizedStorageConfig(config StorageConfig) (StorageConfig, error) {
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	u, err := url.Parse(config.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" || config.Region == "" ||
		config.Bucket == "" || strings.ContainsAny(config.Bucket, "/\\:") ||
		config.Timeout <= 0 || config.Timeout > time.Minute || !validStoragePrefix(config.Prefix) {
		return StorageConfig{}, errors.New("invalid explicit report storage configuration")
	}
	return config, nil
}

func validStoragePrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 1024 || !strings.HasSuffix(prefix, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if !storageSegment.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func ValidateNormalizedPrefix(raw, normalized string) error {
	if !validStoragePrefix(raw) || !validStoragePrefix(normalized) ||
		strings.HasPrefix(raw, normalized) || strings.HasPrefix(normalized, raw) {
		return errors.New("raw and normalized prefixes must be explicit, valid and disjoint")
	}
	return nil
}

func ValidateReadinessKey(prefix, key string) error {
	if !validStoragePrefix(prefix) || !strings.HasPrefix(key, prefix) || len(key) <= len(prefix) || len(key) > 1024 {
		return errors.New("readiness key must identify an object within the configured raw prefix")
	}
	for _, segment := range strings.Split(key, "/") {
		if !storageSegment.MatchString(segment) || segment == "." || segment == ".." {
			return errors.New("invalid readiness object key")
		}
	}
	return nil
}

func evidenceConfig(config StorageConfig) evidence.Config {
	return evidence.Config{Endpoint: config.Endpoint, Bucket: config.Bucket, Prefix: config.Prefix, Region: config.Region,
		AccessKey: config.AccessKey, SecretKey: config.SecretKey, Timeout: config.Timeout}
}

func newReportClient(config StorageConfig) (*s3.Client, *http.Transport) {
	return reportClientWithTransport(config, http.DefaultTransport.(*http.Transport))
}

func reportClientWithTransport(config StorageConfig, supplied *http.Transport) (*s3.Client, *http.Transport) {
	transport := supplied.Clone()
	client := s3.New(s3.Options{
		Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("report storage redirects are disabled")
			}},
		RetryMaxAttempts:           2,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return client, transport
}

func openReportStore(ctx context.Context, config StorageConfig, limit int64) (*reportStore, error) {
	return openReportStoreWithTransport(ctx, config, limit, nil)
}

func openReportStoreWithTransport(ctx context.Context, config StorageConfig, limit int64, transport *http.Transport) (*reportStore, error) {
	config, err := normalizedStorageConfig(config)
	if err != nil {
		return nil, err
	}
	var reader *evidence.Reader
	if transport == nil {
		reader, err = evidence.OpenReader(ctx, evidenceConfig(config))
	} else {
		reader, err = evidence.OpenReaderWithTransport(ctx, evidenceConfig(config), transport)
	}
	if err != nil {
		return nil, err
	}
	var client *s3.Client
	var owned *http.Transport
	if transport == nil {
		client, owned = newReportClient(config)
	} else {
		client, owned = reportClientWithTransport(config, transport)
	}
	s := &reportStore{config: config, limit: limit, reader: reader, client: client, transport: owned}
	s.config.AccessKey, s.config.SecretKey = "", ""
	return s, nil
}

func (s *reportStore) close() {
	s.transport.CloseIdleConnections()
	_ = s.reader.Close()
}

// ProbeStorage checks only an explicitly selected object. It never lists keys
// or requires bucket-wide authority, and is not invoked by worker construction.
func ProbeStorage(ctx context.Context, config StorageConfig, key string) error {
	config, err := normalizedStorageConfig(config)
	if err != nil {
		return err
	}
	if err := ValidateReadinessKey(config.Prefix, key); err != nil {
		return err
	}
	client, transport := newReportClient(config)
	defer transport.CloseIdleConnections()
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(key)}); err != nil {
		return errors.New("scoped storage readiness object is unavailable")
	}
	return nil
}

func reportDigest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

// Report bodies are already bounded at intake. Using a seekable byte reader
// keeps retries exact without staging credentials or reports in temporary files.
func (s *reportStore) put(ctx context.Context, workspace, id string, data []byte) (string, error) {
	if !storageSegment.MatchString(workspace) || !storageSegment.MatchString(id) || int64(len(data)) > s.limit {
		return "", errors.New("invalid report evidence")
	}
	digest := strings.TrimPrefix(reportDigest(data), "sha256:")
	key := s.config.Prefix + workspace + "/" + id + "/" + digest
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.config.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		ContentType: aws.String("application/octet-stream"),
		Metadata:    map[string]string{"sha256": digest},
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return "", errors.New("report evidence publication failed")
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.config.Bucket), Key: aws.String(key)})
	if err != nil || aws.ToInt64(head.ContentLength) != int64(len(data)) {
		return "", errors.New("report evidence publication could not be confirmed")
	}
	return key, nil
}

func (s *reportStore) read(ctx context.Context, record importRecord) ([]byte, error) {
	return readRawReport(ctx, s.reader, s.config, s.limit, record)
}

func readRawReport(ctx context.Context, reader *evidence.Reader, storage StorageConfig, limit int64, record importRecord) ([]byte, error) {
	if record.ReportSize < 0 || record.ReportSize > limit {
		return nil, errors.New("invalid report evidence reference")
	}
	body, err := reader.Open(ctx, record.WorkspaceID, evidence.Ref{
		WorkspaceID: record.WorkspaceID, Bucket: storage.Bucket, Key: record.ReportKey,
		SHA256: record.ReportDigest, SizeBytes: record.ReportSize,
	})
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, record.ReportSize+1))
	if err := errors.Join(readErr, body.Close()); err != nil {
		return nil, err
	}
	return data, nil
}
