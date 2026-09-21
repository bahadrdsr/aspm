package app

import (
	"context"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/bahadrdsr/aspm/internal/providers"
)

type AssessmentWorkerConfig struct {
	Database              DatabaseConfig
	EncryptionKey         []byte `json:"-"`
	WorkerID, Scope       string
	Client                *http.Client `json:"-"`
	LeaseDuration         time.Duration
	AuthorizationInterval time.Duration
	RequestTimeout        time.Duration
	RequestWindow         time.Duration
	MaxConcurrent         int
	RequestsPerWindow     int
	MaxInputBytes         int
	MaxOutputTokens       int
	MaxResponseBytes      int64
}

type AssessmentWorker struct {
	*database
	credentials cipher.AEAD
	client      *http.Client
	workerID    string
	scope       string
	limits      assessmentPoolLimits
	secrets     []string
	lifetime    context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	active      sync.WaitGroup
	closeOnce   sync.Once
}

type assessmentDispatch struct {
	assessmentJobRecord
	token    string
	deadline time.Time
}

var (
	errAssessmentLeaseLost = errors.New("assessment lease or fence is no longer current")
	errAssessmentChanged   = errors.New("assessment authority changed before commit")
)

func ValidateAssessmentWorkerConfig(config AssessmentWorkerConfig) error {
	if err := ValidateDatabaseConfig(config.Database); err != nil {
		return err
	}
	if len(config.EncryptionKey) != 0 && len(config.EncryptionKey) != 32 ||
		!validAssessmentIdentity(config.WorkerID) || !validAssessmentIdentity(config.Scope) ||
		config.LeaseDuration < 250*time.Millisecond || config.LeaseDuration > time.Minute ||
		config.AuthorizationInterval < 10*time.Millisecond || config.AuthorizationInterval > time.Second ||
		config.AuthorizationInterval >= config.LeaseDuration ||
		config.RequestTimeout <= 0 || config.RequestTimeout > 30*time.Second ||
		config.RequestWindow < time.Second || config.RequestWindow > time.Hour ||
		config.MaxConcurrent < 1 || config.MaxConcurrent > 16 ||
		config.RequestsPerWindow < 1 || config.RequestsPerWindow > 1000 ||
		config.MaxInputBytes < 1 || config.MaxInputBytes > assessmentContextLimit ||
		config.MaxOutputTokens < 1 || config.MaxOutputTokens > 32768 ||
		config.MaxResponseBytes < 1 || config.MaxResponseBytes > 128<<10 {
		return errors.New("assessment worker requires explicit bounded scope, identity and execution limits")
	}
	if err := validateSourceNativeClient(config.Client); err != nil {
		return errors.New("assessment worker requires an explicit bounded direct verified-TLS client without cookies")
	}
	return nil
}

// OpenAssessmentWorker owns only its independent database pool and direct HTTP
// transport. It never constructs core handlers, S3 clients or background jobs.
func OpenAssessmentWorker(ctx context.Context, config AssessmentWorkerConfig) (*AssessmentWorker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateAssessmentWorkerConfig(config); err != nil {
		return nil, err
	}
	client, err := assessmentNativeClient(config.Client)
	if err != nil {
		return nil, err
	}
	credentials, err := newIntegrationCipher(config.EncryptionKey)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	limits := assessmentLimits(config)
	if err = db.registerAssessmentScope(ctx, config.Scope, &limits); err != nil {
		_ = db.close()
		client.CloseIdleConnections()
		return nil, err
	}
	secrets := []string{config.Database.DatabaseURL, string(config.EncryptionKey),
		hex.EncodeToString(config.EncryptionKey), base64.StdEncoding.EncodeToString(config.EncryptionKey)}
	if parsed, parseErr := url.Parse(config.Database.DatabaseURL); parseErr == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		secrets = append(secrets, password)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &AssessmentWorker{database: db, credentials: credentials, client: client,
		workerID: config.WorkerID, scope: config.Scope, limits: limits, secrets: secrets,
		lifetime: lifetime, cancel: cancel}, nil
}

func (w *AssessmentWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }

func (w *AssessmentWorker) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.cancel()
		w.mu.Unlock()
		w.active.Wait()
		w.client.CloseIdleConnections()
		_ = w.database.close()
		w.credentials, w.secrets = nil, nil
	})
	return nil
}

func (w *AssessmentWorker) ProcessNext(ctx context.Context) (bool, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false, errors.New("assessment worker is closed")
	}
	w.active.Add(1)
	w.mu.Unlock()
	defer w.active.Done()
	ownedCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.lifetime, cancel)
	defer func() { stop(); cancel() }()
	if err := ownedCtx.Err(); err != nil {
		return false, err
	}
	claimCtx, endClaim := context.WithTimeout(ownedCtx, 2*time.Second)
	dispatch, configuration, worked, err := w.claimAssessment(claimCtx)
	if errors.Is(err, errAssessmentChanged) {
		dispatch, configuration, worked, err = w.claimAssessment(claimCtx)
	}
	endClaim()
	if err != nil || !worked || dispatch.State != "dispatching" {
		return worked, assessmentWorkerError(ownedCtx, err)
	}
	job := dispatch.assessmentJobRecord
	// The result lease may outlive I/O so a timeout receipt can be finalized.
	// Neither that lease nor a late resume extends this native execution cutoff.
	deadlineCtx, endRequest := context.WithDeadline(ownedCtx, dispatch.deadline)
	defer endRequest()
	requestCtx, abortRequest := context.WithCancelCause(deadlineCtx)
	defer abortRequest(context.Canceled)
	state := &assessmentHTTPState{}
	client, transport, err := w.dispatchClient(requestCtx, job.Destination, state)
	var adapter *providers.Assessor
	if err == nil {
		adapter, err = providers.Open(requestCtx, providers.Config{
			Profiles: map[string]providers.Profile{job.ProfileID: configuration.Profile},
			Policy:   configuration.Policy, Client: client, MaxAttempts: 1,
			MaxOutputTokens: w.limits.Output, MaxResponseBytes: w.limits.Response,
		})
	}
	var result providers.Result
	var monitored chan struct{}
	if err == nil {
		err = w.refreshAssessment(deadlineCtx, job)
	}
	if err == nil {
		finished := make(chan struct{})
		monitored = make(chan struct{})
		go func() {
			defer close(monitored)
			w.monitorAssessment(deadlineCtx, job, finished, abortRequest)
		}()
		result, err = adapter.Assess(requestCtx, providers.Request{
			WorkspaceID: job.WorkspaceID, RunID: job.ID, FindingID: job.FindingID,
			ProfileID: job.ProfileID, Task: job.Task, DataClass: job.DataClass,
			PromptRevision: job.PromptRevision, EvidenceID: job.ContextRef,
			EvidenceDigest: job.ContextDigest, EvidenceText: job.Context,
		})
		close(finished)
	}
	// Assess has returned and its deferred response-body Close has run. Close
	// this attempt's actual sockets/dials before acknowledging capacity, even
	// if cancellation or another worker already retired its public job/fence.
	if transport == nil || transport.close() {
		w.releaseAssessmentReservation(dispatch)
	}
	if monitored != nil {
		// Let an already-running bounded authority check finish. Cancelling it
		// merely because a response arrived must not hide a lost lease or denial.
		<-monitored
	}
	if cause := context.Cause(requestCtx); cause != nil {
		err = cause
	}
	secrets := append(append([]string{}, w.secrets...), configuration.Profile.APIKey)
	outcome := assessmentNativeOutcome(job, result, err, state, secrets)
	settleCtx, endSettle := context.WithTimeout(context.Background(), 2*time.Second)
	defer endSettle()
	for attempt := 0; attempt < 2; attempt++ {
		err = w.finalizeAssessment(settleCtx, requestCtx, job, outcome)
		if !errors.Is(err, errAssessmentChanged) {
			break
		}
	}
	if errors.Is(err, errAssessmentLeaseLost) {
		return true, nil
	}
	return true, assessmentWorkerError(settleCtx, err)
}

func (w *AssessmentWorker) releaseAssessmentReservation(dispatch assessmentDispatch) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// This row-only acknowledgment acquires no later workspace/quota locks.
	// The immutable attempt token, not the retired result fence, identifies
	// exactly which I/O ended. Failure conservatively retains the reservation
	// until its persisted deadline; it never refunds the request-window charge.
	_, _ = w.pool.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
		SET io_released_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND io_owner_id=$4 AND io_token=$5
			AND attempts=1 AND io_released_at IS NULL`,
		dispatch.WorkspaceID, dispatch.ID, w.scope, w.workerID, dispatch.token)
}

func assessmentWorkerError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errUnavailable
}
