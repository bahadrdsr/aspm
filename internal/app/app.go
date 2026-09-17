package app

import (
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const APIVersion = "aspm/v1alpha1"

type StorageConfig struct {
	Endpoint, Bucket, Prefix, Region string
	AccessKey                        string `json:"-"`
	SecretKey                        string `json:"-"`
	Timeout                          time.Duration
}

type Config struct {
	DatabaseURL              string `json:"-"`
	Schema, ApplicationName  string
	MaxConnections           int32
	Storage                  StorageConfig
	CollectionStorage        *StorageConfig   `json:"-"`
	BootstrapToken           string           `json:"-"`
	IntegrationEncryptionKey []byte           `json:"-"`
	Now                      func() time.Time `json:"-"`
	LogOutput                io.Writer        `json:"-"`
	SessionTTL               time.Duration
	MaxUploadBytes           int64
	ManualProcessing         bool
	OIDC                     *OIDCConfig
	QueryTracer              pgx.QueryTracer `json:"-"`
	// PublicOrigin is the trusted HTTPS browser origin when TLS terminates at a
	// reverse proxy. Forwarded headers are deliberately not trusted.
	PublicOrigin string
}

type Application struct {
	*database
	Handler                http.Handler
	storage                *reportStore
	config                 Config
	oidc                   *oidcAuth
	imports                *ImportWorker
	integrationCredentials cipher.AEAD
	collectionEvidence     *sourceEvidenceReader

	bootstrapEnabled bool
	bootstrapHash    [32]byte
	hashSlots        chan struct{}
	dummyPassword    string
	cancel           context.CancelFunc
	workers          sync.WaitGroup
	closeOnce        sync.Once
}

var schemaName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Open migrates only app-owned tables in an existing, explicitly selected
// schema. It does not create databases, schemas, buckets, or M02 job tables.
func Open(ctx context.Context, config Config) (*Application, error) {
	credentials, err := newIntegrationCipher(config.IntegrationEncryptionKey)
	if err != nil {
		return nil, err
	}
	config.IntegrationEncryptionKey = nil
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.LogOutput == nil {
		config.LogOutput = io.Discard
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = 8 * time.Hour
	}
	if config.MaxUploadBytes == 0 {
		config.MaxUploadBytes = 8 << 20
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = 4
	}
	if !schemaName.MatchString(config.Schema) || config.DatabaseURL == "" ||
		config.ApplicationName == "" || len(config.ApplicationName) > 63 ||
		config.MaxConnections < 1 || config.MaxConnections > 100 ||
		config.SessionTTL < time.Second || config.SessionTTL > 30*24*time.Hour ||
		config.MaxUploadBytes < 1024 || config.MaxUploadBytes > 32<<20 ||
		(config.BootstrapToken != "" && len(config.BootstrapToken) < 32) {
		return nil, errors.New("invalid application configuration")
	}
	if config.PublicOrigin != "" {
		u, err := url.Parse(config.PublicOrigin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("public origin must be an HTTPS origin without a path")
		}
	}
	dbConfig := DatabaseConfig{DatabaseURL: config.DatabaseURL, Schema: config.Schema,
		ApplicationName: config.ApplicationName, MaxConnections: config.MaxConnections, Now: config.Now, LogOutput: config.LogOutput,
		QueryTracer: config.QueryTracer}
	if err := ValidateDatabaseConfig(dbConfig); err != nil {
		return nil, err
	}
	// Construction validates explicit storage configuration without a network
	// probe, before any database connection or migration.
	storage, err := openReportStore(ctx, config.Storage, config.MaxUploadBytes)
	if err != nil {
		return nil, err
	}
	a := &Application{
		config: config, storage: storage,
		integrationCredentials: credentials,
		bootstrapEnabled:       config.BootstrapToken != "",
		bootstrapHash:          sha256.Sum256([]byte(config.BootstrapToken)),
		hashSlots:              make(chan struct{}, 2),
	}
	a.config.BootstrapToken = ""
	if config.CollectionStorage != nil {
		a.collectionEvidence, err = openSourceEvidenceReader(ctx, *config.CollectionStorage)
		a.config.CollectionStorage = nil
		if err != nil {
			a.storage.close()
			return nil, err
		}
	}
	a.database, err = openDatabase(ctx, dbConfig)
	if err != nil {
		a.collectionEvidence.close()
		a.storage.close()
		return nil, err
	}
	a.imports = &ImportWorker{database: a.database, reader: a.storage.reader,
		storage: a.storage.config, maxUploadBytes: config.MaxUploadBytes}
	a.dummyPassword, err = a.hashPassword(ctx, randomToken())
	if err != nil {
		a.collectionEvidence.close()
		a.storage.close()
		a.pool.Close()
		return nil, errors.New("initialize local authentication failed")
	}
	a.oidc = newOIDCAuth(config.OIDC)
	a.config.OIDC = nil
	workerCtx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.Handler = http.HandlerFunc(a.serveHTTP)
	if !config.ManualProcessing {
		a.workers.Add(1)
		go func() {
			defer a.workers.Done()
			a.imports.runWorker(workerCtx)
		}()
	}
	return a, nil
}

func (a *Application) Close() error {
	a.closeOnce.Do(func() {
		a.cancel()
		a.workers.Wait()
		if a.oidc != nil {
			a.oidc.close()
		}
		a.storage.close()
		a.collectionEvidence.close()
		_ = a.database.close()
	})
	return nil
}

func (a *Application) Ping(ctx context.Context) error { return a.database.ping(ctx) }

func (a *Application) ProcessImports(ctx context.Context) error { return a.imports.ProcessImports(ctx) }

func (a *Application) ProcessReports(ctx context.Context) error {
	return (&ReportWorker{database: a.database}).ProcessReports(ctx)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
