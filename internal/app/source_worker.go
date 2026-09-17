package app

import (
	"context"
	"crypto/cipher"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type CollectionWorkerConfig struct {
	Database       DatabaseConfig
	Storage        StorageConfig
	EncryptionKey  []byte `json:"-"`
	WorkerID       string
	LeaseDuration  time.Duration
	GitHubEndpoint string       `json:"-"`
	Client         *http.Client `json:"-"`
	Limits         connectors.Limits
}

type CollectionWorker struct {
	*database
	store       *reportStore
	credentials cipher.AEAD
	client      *http.Client
	endpoint    string
	workerID    string
	lease       time.Duration
	limits      connectors.Limits
	lifetime    context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	active      sync.WaitGroup
	closeOnce   sync.Once
}

type collectionDenial string

func (reason collectionDenial) Error() string { return "source collection: " + string(reason) }

const (
	sourceAuthorizationRevoked collectionDenial = "authorization-revoked"
	sourceConnectionDisabled   collectionDenial = "connection-disabled"
	sourceConnectionChanged    collectionDenial = "connection-changed"
	sourceLeaseLost            collectionDenial = "lease-lost"
	sourceIdentityConflict     collectionDenial = "identity-conflict"
)

// NormalizeCollectionLimits validates native bounds and supplies their existing defaults.
func NormalizeCollectionLimits(limits connectors.Limits) (connectors.Limits, error) {
	if limits.Requests < 0 || limits.Requests > 128 || limits.Pages < 0 || limits.Pages > 64 ||
		limits.PageSize < 0 || limits.PageSize > 100 || limits.Bytes < 0 || limits.Bytes > sourceEvidenceLimit {
		return limits, errors.New("invalid bounded source collection limits")
	}
	if limits.Requests == 0 {
		limits.Requests = 32
	}
	if limits.Pages == 0 {
		limits.Pages = 8
	}
	if limits.PageSize == 0 {
		limits.PageSize = 50
	}
	if limits.Bytes == 0 {
		limits.Bytes = 8 << 20
	}
	return limits, nil
}

func validateSourceNativeClient(client *http.Client) error {
	if client == nil {
		return errors.New("source collection requires an approved HTTP client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil || transport.DialTLS != nil || transport.DialTLSContext != nil {
		return errors.New("source collection requires a direct normal-TLS transport without proxies")
	}
	if transport.Proxy != nil {
		return errors.New("source collection proxy transports are not permitted")
	}
	if policy := transport.TLSClientConfig; policy != nil {
		minimum, maximum := policy.MinVersion, policy.MaxVersion
		if minimum == 0 {
			minimum = tls.VersionTLS12
		}
		if maximum == 0 {
			maximum = tls.VersionTLS13
		}
		if policy.InsecureSkipVerify || minimum < tls.VersionTLS12 || minimum > tls.VersionTLS13 ||
			maximum < minimum || maximum < tls.VersionTLS12 || policy.ServerName != "" {
			return errors.New("source collection requires verified TLS for the selected hostname")
		}
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second || client.Jar != nil {
		return errors.New("source collection requires a bounded client without a cookie jar")
	}
	return nil
}

func sourceNativeClient(client *http.Client, endpoint string) (*http.Client, error) {
	if err := validateSourceNativeClient(client); err != nil {
		return nil, err
	}
	transport := client.Transport.(*http.Transport)
	origin, err := sourceOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	owned := transport.Clone()
	if owned.TLSClientConfig == nil {
		owned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		owned.TLSClientConfig = owned.TLSClientConfig.Clone()
		if owned.TLSClientConfig.RootCAs != nil {
			owned.TLSClientConfig.RootCAs = owned.TLSClientConfig.RootCAs.Clone()
		}
		if owned.TLSClientConfig.MinVersion == 0 {
			owned.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	dial := owned.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	owned.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !strings.EqualFold(address, origin) {
			return nil, errors.New("source collection denied a different network origin")
		}
		return dial(ctx, network, address)
	}
	copy := *client
	copy.Transport = owned
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy, nil
}

// ValidateCollectionGateway preserves the selected HTTPS base or the fixed native default.
func ValidateCollectionGateway(endpoint string) (string, error) {
	if endpoint == "" {
		endpoint = "https://api.github.com"
	}
	endpoint, err := ValidateDeliveryGateway(endpoint)
	if err != nil {
		return "", errors.New("collection worker requires a valid trusted HTTPS gateway")
	}
	return endpoint, nil
}

// ValidateCollectionWorkerConfig performs the constructor's checks without opening resources.
func ValidateCollectionWorkerConfig(config CollectionWorkerConfig) error {
	if len(config.EncryptionKey) != 32 {
		return errors.New("collection worker requires an explicit 32-byte encryption key")
	}
	if !validText(config.WorkerID, 128) || strings.TrimSpace(config.WorkerID) != config.WorkerID ||
		strings.ContainsFunc(config.WorkerID, unicode.IsControl) ||
		config.LeaseDuration < 250*time.Millisecond || config.LeaseDuration > time.Minute {
		return errors.New("collection worker requires a bounded identity and a lease from 250ms to one minute")
	}
	if err := ValidateDatabaseConfig(config.Database); err != nil {
		return err
	}
	if err := ValidateStorageConfig(config.Storage); err != nil {
		return err
	}
	if _, err := NormalizeCollectionLimits(config.Limits); err != nil {
		return err
	}
	if _, err := ValidateCollectionGateway(config.GitHubEndpoint); err != nil {
		return err
	}
	return validateSourceNativeClient(config.Client)
}

func OpenCollectionWorker(ctx context.Context, config CollectionWorkerConfig) (*CollectionWorker, error) {
	if err := ValidateCollectionWorkerConfig(config); err != nil {
		return nil, err
	}
	storage, err := normalizedStorageConfig(config.Storage)
	if err != nil {
		return nil, err
	}
	limits, err := NormalizeCollectionLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	endpoint, err := ValidateCollectionGateway(config.GitHubEndpoint)
	if err != nil {
		return nil, err
	}
	client, err := sourceNativeClient(config.Client, endpoint)
	if err != nil {
		return nil, err
	}
	credentials, err := newIntegrationCipher(config.EncryptionKey)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	transport, err := sourceDirectTransport(storage.Endpoint)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	defer transport.CloseIdleConnections()
	store, err := openReportStoreWithTransport(ctx, storage, limits.Bytes, transport)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		store.close()
		client.CloseIdleConnections()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &CollectionWorker{database: db, store: store, credentials: credentials, client: client,
		endpoint: endpoint, workerID: config.WorkerID, lease: config.LeaseDuration, limits: limits,
		lifetime: lifetime, cancel: cancel}, nil
}

func (w *CollectionWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }

func (w *CollectionWorker) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.cancel()
		w.mu.Unlock()
		w.active.Wait()
		w.store.close()
		w.client.CloseIdleConnections()
		_ = w.database.close()
		w.credentials = nil
	})
	return nil
}

func (w *CollectionWorker) sourceAuthority(ctx context.Context, db queryRower, job sourceCollectionRecord, lock bool) (sourceConnectionRecord, error) {
	var source sourceConnectionRecord
	if lock {
		var id string
		if err := db.QueryRow(ctx, `SELECT id FROM `+w.table("workspaces")+` WHERE id=$1 FOR KEY SHARE`, job.WorkspaceID).Scan(&id); err != nil {
			return source, err
		}
	}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var role string
	err := db.QueryRow(ctx, `SELECT role FROM `+w.table("memberships")+`
		WHERE workspace_id=$1 AND user_id=$2`+suffix, job.WorkspaceID, job.RequestedBy).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canWrite(Workspace{Role: role}) {
		return source, sourceAuthorizationRevoked
	}
	if err != nil {
		return source, err
	}
	if lock {
		suffix = " FOR UPDATE"
	}
	source, err = scanSourceConnection(db.QueryRow(ctx, `SELECT `+sourceConnectionColumns+
		` FROM `+w.table("source_connections")+` WHERE workspace_id=$1 AND id=$2`+suffix, job.WorkspaceID, job.SourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return source, sourceConnectionChanged
	}
	if err != nil {
		return source, err
	}
	if !source.Enabled {
		return source, sourceConnectionDisabled
	}
	if source.Revision != job.ConnectionRevision || source.Repository != job.Repository || source.Profile != job.Profile {
		return source, sourceConnectionChanged
	}
	return source, nil
}

func (w *CollectionWorker) sourceLease(ctx context.Context, db queryRower, job sourceCollectionRecord) error {
	var live bool
	err := db.QueryRow(ctx, `SELECT state='collecting' AND worker_id=$3 AND fence=$4 AND lease_until>clock_timestamp()
		FROM `+w.table("source_collections")+` WHERE workspace_id=$1 AND id=$2`,
		job.WorkspaceID, job.ID, job.workerID, job.fence).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !live {
		return sourceLeaseLost
	}
	return err
}

func sourceFailure(err error) (string, *FindingDeliveryFailure) {
	state, code := "failed", "unavailable"
	var denial collectionDenial
	if errors.As(err, &denial) {
		code = string(denial)
		if denial == sourceAuthorizationRevoked || denial == sourceConnectionChanged || denial == sourceConnectionDisabled {
			state = "blocked"
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = "canceled"
	}
	return state, &FindingDeliveryFailure{Code: code}
}

func (w *CollectionWorker) settleSource(ctx context.Context, job sourceCollectionRecord, state string, failure *FindingDeliveryFailure) error {
	raw, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	result, err := w.pool.Exec(ctx, `UPDATE `+w.table("source_collections")+`
		SET state=$5,complete=false,asset_id=NULL,repository_id=NULL,record_count=0,gaps='[]',
			failure=$6,completed_at=clock_timestamp(),lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4 AND state='collecting'`,
		job.WorkspaceID, job.ID, job.workerID, job.fence, state, raw)
	if err != nil {
		return deliveryInfrastructureError(ctx, "persist source collection failure failed")
	}
	if result.RowsAffected() != 1 {
		return sourceLeaseLost
	}
	return nil
}

func (w *CollectionWorker) failSource(job sourceCollectionRecord, cause error) error {
	state, failure := sourceFailure(cause)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return w.settleSource(ctx, job, state, failure)
}

func (w *CollectionWorker) claimSource(ctx context.Context) (sourceCollectionRecord, sourceConnectionRecord, bool, error) {
	var job sourceCollectionRecord
	var source sourceConnectionRecord
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return job, source, false, deliveryInfrastructureError(ctx, "open source claim failed")
	}
	defer rollback(tx)
	job, err = scanSourceCollection(tx.QueryRow(ctx, `SELECT `+sourceCollectionColumns+` FROM `+w.table("source_collections")+`
		WHERE state='queued' OR (state='collecting' AND lease_until<=clock_timestamp())
		ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, pgx.ErrNoRows) {
		return job, source, false, nil
	}
	if err != nil {
		return job, source, false, deliveryInfrastructureError(ctx, "read source claim failed")
	}
	if job.State == "collecting" {
		failure, marshalErr := json.Marshal(FindingDeliveryFailure{Code: "lease-expired"})
		if marshalErr != nil {
			return job, source, false, errors.New("encode source lease failure failed")
		}
		_, err = tx.Exec(ctx, `UPDATE `+w.table("source_collections")+`
			SET state='failed',complete=false,failure=$3,completed_at=clock_timestamp(),
				worker_id=$4,fence=fence+1,lease_until=NULL
			WHERE workspace_id=$1 AND id=$2`, job.WorkspaceID, job.ID, failure, w.workerID)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			return job, source, false, deliveryInfrastructureError(ctx, "settle expired source collection failed")
		}
		return sourceCollectionRecord{}, source, true, nil
	}
	source, err = w.sourceAuthority(ctx, tx, job, true)
	if err != nil {
		var denial collectionDenial
		if !errors.As(err, &denial) {
			return job, source, false, deliveryInfrastructureError(ctx, "check current source authority failed")
		}
		state, failure := sourceFailure(err)
		raw, marshalErr := json.Marshal(failure)
		if marshalErr != nil {
			return job, source, false, errors.New("encode source authorization failure failed")
		}
		_, err = tx.Exec(ctx, `UPDATE `+w.table("source_collections")+`
			SET state=$3,failure=$4,completed_at=clock_timestamp(),worker_id=$5,fence=fence+1
			WHERE workspace_id=$1 AND id=$2`, job.WorkspaceID, job.ID, state, raw, w.workerID)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			return job, source, false, deliveryInfrastructureError(ctx, "settle blocked source collection failed")
		}
		return sourceCollectionRecord{}, source, true, nil
	}
	job, err = scanSourceCollection(tx.QueryRow(ctx, `UPDATE `+w.table("source_collections")+`
		SET state='collecting',worker_id=$3,fence=fence+1,lease_until=clock_timestamp()+($4*interval '1 millisecond')
		WHERE workspace_id=$1 AND id=$2 RETURNING `+sourceCollectionColumns,
		job.WorkspaceID, job.ID, w.workerID, w.lease.Milliseconds()))
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return job, source, false, deliveryInfrastructureError(ctx, "commit source collection claim failed")
	}
	return job, source, true, nil
}

func (w *CollectionWorker) watchSource(ctx context.Context, cancel context.CancelCauseFunc, job sourceCollectionRecord) func() {
	watch, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				_, err := w.sourceAuthority(watch, w.pool, job, false)
				if err == nil {
					err = w.sourceLease(watch, w.pool, job)
				}
				if err != nil {
					if watch.Err() == nil {
						cancel(err)
					}
					return
				}
			}
		}
	}()
	return func() { stop(); <-done }
}
