package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

type retentionStore struct {
	client           *s3.Client
	transport        *http.Transport
	rawReader        *evidence.Reader
	archiveReader    *evidence.Reader
	raw, archive     StorageConfig
	maxEvidenceBytes int64
}

func validateRetentionStorage(raw, archive StorageConfig) (StorageConfig, StorageConfig, error) {
	raw, err := normalizedStorageConfig(raw)
	if err != nil {
		return StorageConfig{}, StorageConfig{}, err
	}
	archive, err = normalizedStorageConfig(archive)
	if err != nil {
		return StorageConfig{}, StorageConfig{}, err
	}
	if raw.Endpoint != archive.Endpoint || raw.Bucket != archive.Bucket || raw.Region != archive.Region ||
		raw.AccessKey != archive.AccessKey || raw.SecretKey != archive.SecretKey ||
		strings.HasPrefix(raw.Prefix, archive.Prefix) || strings.HasPrefix(archive.Prefix, raw.Prefix) {
		return StorageConfig{}, StorageConfig{}, errors.New("retention raw and archive storage must use one explicit credential with disjoint prefixes")
	}
	return raw, archive, nil
}

func openRetentionStore(ctx context.Context, raw, archive StorageConfig, limit int64) (*retentionStore, error) {
	raw, archive, err := validateRetentionStorage(raw, archive)
	if err != nil || limit < 1024 || limit > 32<<20 {
		return nil, errors.New("invalid retention storage configuration")
	}
	rawReader, err := evidence.OpenReader(ctx, evidenceConfig(raw))
	if err != nil {
		return nil, err
	}
	archiveReader, err := evidence.OpenReader(ctx, evidenceConfig(archive))
	if err != nil {
		_ = rawReader.Close()
		return nil, err
	}
	client, transport := newReportClient(raw)
	store := &retentionStore{
		client: client, transport: transport, rawReader: rawReader, archiveReader: archiveReader,
		raw: raw, archive: archive, maxEvidenceBytes: limit,
	}
	store.raw.AccessKey, store.raw.SecretKey = "", ""
	store.archive.AccessKey, store.archive.SecretKey = "", ""
	return store, nil
}

func (s *retentionStore) close() error {
	s.transport.CloseIdleConnections()
	return errors.Join(s.rawReader.Close(), s.archiveReader.Close())
}

func (s *retentionStore) scopedKey(config StorageConfig, workspace, key string) bool {
	return storageSegment.MatchString(workspace) && strings.HasPrefix(key, config.Prefix+workspace+"/") &&
		!strings.Contains(key, "\\") && !strings.Contains(key, "/../") && !strings.Contains(key, "/./")
}

func readRetentionObject(ctx context.Context, reader *evidence.Reader, config StorageConfig,
	workspace, key, digest string, size int64, limit int64) ([]byte, error) {
	if size < 0 || size > limit {
		return nil, evidence.ErrInvalid
	}
	body, err := reader.Open(ctx, workspace, evidence.Ref{
		WorkspaceID: workspace, Bucket: config.Bucket, Key: key, SHA256: digest, SizeBytes: size,
	})
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, size+1))
	return data, errors.Join(readErr, body.Close())
}

func (s *retentionStore) readRaw(ctx context.Context, record importRecord) ([]byte, error) {
	if !s.scopedKey(s.raw, record.WorkspaceID, record.ReportKey) {
		return nil, evidence.ErrScope
	}
	return readRetentionObject(ctx, s.rawReader, s.raw, record.WorkspaceID, record.ReportKey,
		record.ReportDigest, record.ReportSize, s.maxEvidenceBytes)
}

func (s *retentionStore) readArchive(ctx context.Context, workspace, key, digest string, size int64) ([]byte, error) {
	if !s.scopedKey(s.archive, workspace, key) {
		return nil, evidence.ErrScope
	}
	return readRetentionObject(ctx, s.archiveReader, s.archive, workspace, key, digest, size, s.maxEvidenceBytes)
}

func (s *retentionStore) putArchive(ctx context.Context, workspace, kind, id string, data []byte) (string, string, error) {
	key, digest, err := s.archiveIdentity(workspace, kind, id, data)
	if err != nil {
		return "", "", err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.archive.Bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))), ContentType: aws.String("application/json"),
		Metadata: map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")},
	})
	if err != nil {
		return "", "", fmt.Errorf("archive evidence publication failed: %w", err)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.archive.Bucket), Key: aws.String(key),
	})
	if err != nil || aws.ToInt64(head.ContentLength) != int64(len(data)) {
		return "", "", errors.New("archive evidence publication could not be confirmed")
	}
	verified, err := readRetentionObject(ctx, s.archiveReader, s.archive, workspace, key,
		digest, int64(len(data)), s.maxEvidenceBytes)
	if err != nil || !bytes.Equal(verified, data) {
		return "", "", errors.New("archive evidence publication failed exact verification")
	}
	return key, digest, nil
}

func (s *retentionStore) archiveIdentity(workspace, kind, id string, data []byte) (string, string, error) {
	if !storageSegment.MatchString(workspace) || !storageSegment.MatchString(kind) ||
		!validID(id) || int64(len(data)) > s.maxEvidenceBytes {
		return "", "", evidence.ErrInvalid
	}
	digest := reportDigest(data)
	key := s.archive.Prefix + workspace + "/" + kind + "/" + id + "/" + strings.TrimPrefix(digest, "sha256:")
	return key, digest, nil
}

func (s *retentionStore) deleteRaw(ctx context.Context, workspace, key string) error {
	return s.delete(ctx, s.raw, workspace, key)
}

func (s *retentionStore) deleteArchive(ctx context.Context, workspace, key string) error {
	return s.delete(ctx, s.archive, workspace, key)
}

func (s *retentionStore) delete(ctx context.Context, config StorageConfig, workspace, key string) error {
	if !s.scopedKey(config, workspace, key) {
		return evidence.ErrScope
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(config.Bucket), Key: aws.String(key),
	})
	return err
}
