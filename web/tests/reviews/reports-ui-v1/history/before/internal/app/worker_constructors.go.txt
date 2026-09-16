package app

import (
	"context"
	"errors"
	"sync"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

type ImportWorkerConfig struct {
	Database         DatabaseConfig
	Storage          StorageConfig
	NormalizedPrefix string
	MaxUploadBytes   int64
}

// ImportWorker has a read-only raw-evidence capability and no HTTP/auth surface.
// Normalized observations are committed to PostgreSQL, not written into raw S3.
type ImportWorker struct {
	*database
	reader         *evidence.Reader
	storage        StorageConfig
	maxUploadBytes int64
	closeOnce      sync.Once
	closeErr       error
}

func OpenImportWorker(ctx context.Context, config ImportWorkerConfig) (*ImportWorker, error) {
	if config.MaxUploadBytes == 0 {
		config.MaxUploadBytes = 8 << 20
	}
	if config.MaxUploadBytes < 1024 || config.MaxUploadBytes > 32<<20 {
		return nil, errors.New("invalid import byte limit")
	}
	if err := ValidateDatabaseConfig(config.Database); err != nil {
		return nil, err
	}
	if err := ValidateNormalizedPrefix(config.Storage.Prefix, config.NormalizedPrefix); err != nil {
		return nil, err
	}
	storage, err := normalizedStorageConfig(config.Storage)
	if err != nil {
		return nil, err
	}
	reader, err := evidence.OpenReader(ctx, evidenceConfig(storage))
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		return nil, errors.Join(err, reader.Close())
	}
	return &ImportWorker{database: db, reader: reader, storage: storage, maxUploadBytes: config.MaxUploadBytes}, nil
}

func (w *ImportWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }

func (w *ImportWorker) Close() error {
	w.closeOnce.Do(func() { w.closeErr = errors.Join(w.reader.Close(), w.database.close()) })
	return w.closeErr
}

func (w *ImportWorker) readReport(ctx context.Context, record importRecord) ([]byte, error) {
	return readRawReport(ctx, w.reader, w.storage, w.maxUploadBytes, record)
}

// ReportWorker owns only a database connection pool. Construction never creates
// a storage client, authentication component, or application HTTP handler.
type ReportWorker struct{ *database }

func OpenReportWorker(ctx context.Context, config DatabaseConfig) (*ReportWorker, error) {
	db, err := openDatabase(ctx, config)
	if err != nil {
		return nil, err
	}
	return &ReportWorker{database: db}, nil
}

func (w *ReportWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }
func (w *ReportWorker) Close() error                   { return w.database.close() }
