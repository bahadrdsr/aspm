package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var (
	ErrInvalid   = errors.New("invalid evidence input")
	ErrNotFound  = errors.New("evidence not found")
	ErrScope     = errors.New("evidence scope mismatch")
	ErrIntegrity = errors.New("evidence integrity mismatch")
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Ref struct {
	WorkspaceID string
	Bucket      string
	Key         string
	SHA256      string
	SizeBytes   int64
}

type Config struct {
	Endpoint  string
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
	Bucket    string
	Prefix    string
	Region    string
	Timeout   time.Duration
}

type Store struct {
	client    *s3.Client
	transport *http.Transport
	config    Config
}

func Open(ctx context.Context, config Config) (*Store, error) {
	s, err := newStore(config)
	if err != nil {
		return nil, err
	}
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(config.Bucket)}); err != nil {
		s.transport.CloseIdleConnections()
		return nil, fmt.Errorf("open evidence bucket: %w", serviceError(err))
	}
	return s, nil
}

func newStore(config Config) (*Store, error) {
	return newStoreWithTransport(config, nil)
}

func newStoreWithTransport(config Config, supplied *http.Transport) (*Store, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		config.Bucket == "" || strings.ContainsAny(config.Bucket, "/\\:") ||
		strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" || config.Region == "" ||
		config.Timeout <= 0 || !validPrefix(config.Prefix) {
		return nil, ErrInvalid
	}
	var transport *http.Transport
	if supplied == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		transport = supplied.Clone()
	}
	client := &http.Client{
		Transport: transport, Timeout: config.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("evidence endpoint redirects are not permitted")
		},
	}
	s := &Store{config: config, transport: transport}
	s.client = s3.New(s3.Options{
		Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient:  client, RetryMaxAttempts: 2,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return s, nil
}

func validPrefix(prefix string) bool {
	if prefix == "" || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if !safeSegment.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Store) Close() error {
	s.transport.CloseIdleConnections()
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.config.Bucket)})
	if err != nil {
		return fmt.Errorf("evidence bucket unavailable: %w", serviceError(err))
	}
	return nil
}

// Put stages bounded input on disk so hashing and retryable S3 writes do not
// require a report-sized allocation or publish partially read evidence.
func (s *Store) Put(ctx context.Context, workspace, objectID string, input io.Reader, size int64) (ref Ref, err error) {
	if !safeSegment.MatchString(workspace) || !safeSegment.MatchString(objectID) || size < 0 || size > 8<<30 || input == nil {
		return Ref{}, ErrInvalid
	}
	file, err := os.CreateTemp("", "aspm-evidence-*")
	if err != nil {
		return Ref{}, fmt.Errorf("stage evidence: %w", err)
	}
	name := file.Name()
	defer func() {
		err = errors.Join(err, file.Close(), os.Remove(name))
		if err != nil {
			ref = Ref{}
		}
	}()
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(&contextReader{ctx: ctx, reader: input}, size+1))
	if err != nil {
		return Ref{}, fmt.Errorf("read evidence input: %w", err)
	}
	if n != size {
		return Ref{}, fmt.Errorf("%w: input size differs from declared size", ErrIntegrity)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Ref{}, fmt.Errorf("rewind staged evidence: %w", err)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	key := s.config.Prefix + workspace + "/" + objectID + "/" + digest
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.config.Bucket), Key: aws.String(key), Body: file,
		ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"),
		Metadata: map[string]string{"sha256": digest},
	})
	if err != nil {
		return Ref{}, fmt.Errorf("publish evidence: %w", serviceError(err))
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.config.Bucket), Key: aws.String(key)})
	if err != nil {
		return Ref{}, fmt.Errorf("confirm evidence publication: %w", serviceError(err))
	}
	if aws.ToInt64(head.ContentLength) != size {
		return Ref{}, ErrIntegrity
	}
	return Ref{WorkspaceID: workspace, Bucket: s.config.Bucket, Key: key, SHA256: "sha256:" + digest, SizeBytes: size}, nil
}

func (s *Store) Open(ctx context.Context, workspace string, ref Ref) (io.ReadCloser, error) {
	if !safeSegment.MatchString(workspace) || ref.WorkspaceID != workspace || ref.Bucket != s.config.Bucket ||
		!strings.HasPrefix(ref.Key, s.config.Prefix+workspace+"/") ||
		strings.Contains(ref.Key, "\\") || strings.Contains(ref.Key, "/../") || strings.Contains(ref.Key, "/./") {
		return nil, ErrScope
	}
	if !digestPattern.MatchString(ref.SHA256) || ref.SizeBytes < 0 {
		return nil, ErrInvalid
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)})
	if err != nil {
		return nil, fmt.Errorf("open evidence: %w", serviceError(err))
	}
	if aws.ToInt64(result.ContentLength) != ref.SizeBytes {
		return nil, errors.Join(ErrIntegrity, result.Body.Close())
	}
	return &verifiedReader{body: result.Body, hash: sha256.New(), expected: ref}, nil
}

func serviceError(err error) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return ErrNotFound
		case "AccessDenied", "AccessDeniedException", "Forbidden":
			return fmt.Errorf("%w: S3 access denied", ErrScope)
		default:
			return fmt.Errorf("S3 operation failed (%s)", api.ErrorCode())
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("S3 transport failed; inspect endpoint connectivity and credentials")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type verifiedReader struct {
	body     io.ReadCloser
	hash     hash.Hash
	expected Ref
	count    int64
	done     bool
	closed   bool
	failure  error
}

func (r *verifiedReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, errors.New("evidence reader is closed")
	}
	if r.done {
		if r.failure != nil {
			return 0, r.failure
		}
		return 0, io.EOF
	}
	n, err := r.body.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
		r.count += int64(n)
	}
	if r.count > r.expected.SizeBytes {
		r.done, r.failure = true, ErrIntegrity
		return n, r.failure
	}
	if errors.Is(err, io.EOF) {
		r.done = true
		if r.count != r.expected.SizeBytes || "sha256:"+hex.EncodeToString(r.hash.Sum(nil)) != r.expected.SHA256 {
			r.failure = ErrIntegrity
			return n, r.failure
		}
	} else if err != nil {
		r.done, r.failure = true, err
	}
	return n, err
}

func (r *verifiedReader) Close() error {
	if r.closed {
		return r.failure
	}
	if !r.done {
		_, err := io.Copy(io.Discard, r)
		r.failure = errors.Join(r.failure, err)
	}
	r.closed = true
	r.failure = errors.Join(r.failure, r.body.Close())
	return r.failure
}
