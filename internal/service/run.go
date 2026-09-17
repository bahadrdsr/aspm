package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/devhost"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

type roleRuntime struct {
	api     http.Handler
	checks  []func(context.Context) error
	workers []func(context.Context) error
	closers []func() error
}

func (r *roleRuntime) close() error {
	var err error
	for i := len(r.closers) - 1; i >= 0; i-- {
		err = errors.Join(err, r.closers[i]())
	}
	return err
}

func applicationLabel(name string) string {
	const suffix = "-application"
	return name[:min(len(name), 63-len(suffix))] + suffix
}

func openRole(ctx context.Context, role string, config Config) (runtime *roleRuntime, err error) {
	runtime = &roleRuntime{}
	defer func() {
		if err != nil {
			err = errors.Join(err, runtime.close())
			runtime = nil
		}
	}()
	if role == "delivery" {
		worker, openErr := app.OpenDeliveryWorker(ctx, deliveryWorkerConfig(config))
		if openErr != nil {
			return runtime, openErr
		}
		runtime.closers = append(runtime.closers, worker.Close)
		runtime.checks = append(runtime.checks, worker.Ping)
		runtime.workers = append(runtime.workers, func(ctx context.Context) error { return runDelivery(ctx, worker) })
		return runtime, nil
	}
	if role == "reports" {
		worker, openErr := app.OpenReportWorker(ctx, databaseConfig(config.Jobs))
		if openErr != nil {
			return runtime, openErr
		}
		runtime.closers = append(runtime.closers, worker.Close)
		runtime.checks = append(runtime.checks, worker.Ping)
		runtime.workers = append(runtime.workers, func(ctx context.Context) error {
			drain(ctx, "report", worker.ProcessReports)
			return nil
		})
		return runtime, nil
	}
	jobConfig := config.Jobs
	jobConfig.MaxConnections = max(1, config.Jobs.MaxConnections/3)
	queue, err := jobs.Open(ctx, jobConfig)
	if err != nil {
		return runtime, fmt.Errorf("initialize durable jobs: %w", err)
	}
	runtime.closers = append(runtime.closers, queue.Close)
	runtime.checks = append(runtime.checks, queue.Ping)
	db := databaseConfig(config.Jobs)
	db.ApplicationName = applicationLabel(db.ApplicationName)
	db.MaxConnections -= jobConfig.MaxConnections
	if role == "core" {
		application, openErr := app.Open(ctx, app.Config{
			DatabaseURL: db.DatabaseURL, Schema: db.Schema, ApplicationName: db.ApplicationName,
			MaxConnections: db.MaxConnections, Storage: storageConfig(config.Evidence),
			BootstrapToken: config.BootstrapToken, PublicOrigin: config.PublicOrigin,
			IntegrationEncryptionKey: config.IntegrationEncryptionKey,
			Now:                      db.Now, LogOutput: db.LogOutput, SessionTTL: 8 * time.Hour, MaxUploadBytes: 8 << 20,
			ManualProcessing: true,
		})
		if openErr != nil {
			return runtime, fmt.Errorf("initialize core application: %w", openErr)
		}
		runtime.api = application.Handler
		runtime.closers = append(runtime.closers, application.Close)
		runtime.checks = append(runtime.checks, application.Ping)
		return runtime, nil
	}
	worker, err := app.OpenImportWorker(ctx, app.ImportWorkerConfig{
		Database: db, Storage: storageConfig(config.Evidence), NormalizedPrefix: config.NormalizedPrefix, MaxUploadBytes: 8 << 20,
	})
	if err != nil {
		return runtime, fmt.Errorf("initialize import worker: %w", err)
	}
	runtime.closers = append(runtime.closers, worker.Close)
	runtime.checks = append(runtime.checks, worker.Ping)
	reader, err := evidence.OpenReader(ctx, config.Evidence)
	if err != nil {
		return runtime, fmt.Errorf("initialize integrity reader: %w", err)
	}
	runtime.closers = append(runtime.closers, reader.Close)
	runtime.workers = append(runtime.workers,
		func(ctx context.Context) error { drain(ctx, "import", worker.ProcessImports); return nil },
		func(ctx context.Context) error { work(ctx, queue, reader, config); return nil })
	return runtime, nil
}

func Run(ctx context.Context, role string, config Config) (err error) {
	if role == "delivery" {
		config.IntegrationEncryptionKey = bytes.Clone(config.IntegrationEncryptionKey)
		defer clear(config.IntegrationEncryptionKey)
	}
	if err := validateConfig(role, config); err != nil {
		return err
	}
	if role == "delivery" {
		config.DeliveryClient, err = ownDeliveryClient(config)
		if err != nil {
			return err
		}
		defer config.DeliveryClient.CloseIdleConnections()
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(config.TLSCertFile, config.TLSKeyFile)
		if err != nil {
			return errors.New("load service TLS certificate/key failed")
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	var ui http.Handler
	if role == "core" {
		root, openErr := os.OpenRoot(config.Assets)
		if openErr != nil {
			return errors.New("open built UI failed")
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		if _, statErr := fs.Stat(root.FS(), "index.html"); errors.Is(statErr, fs.ErrNotExist) {
			slog.Warn("built UI is unavailable; core API remains available")
			ui = http.NotFoundHandler()
		} else {
			ui, err = devhost.New(root.FS())
			if err != nil {
				return err
			}
		}
	}
	if role != "reports" && role != "delivery" && config.ReadinessKey != "" {
		if config.PrepareReadiness {
			if err := evidence.PrepareReadiness(startup, config.Evidence, config.ReadinessKey); err != nil {
				return err
			}
		} else {
			if err := app.ProbeStorage(startup, storageConfig(config.Evidence), config.ReadinessKey); err != nil {
				return err
			}
		}
	}
	if err := EnsureSchema(startup, config.Jobs); err != nil {
		return err
	}
	runtime, err := openRole(startup, role, config)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.close()) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]string{"apiVersion": jobs.Version, "service": role, "status": "alive"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		for _, probe := range runtime.checks {
			if err := probe(check); err != nil {
				jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"service": role, "status": "database-unavailable"})
				return
			}
		}
		storage := "not-required"
		if role != "reports" && role != "delivery" {
			storage = "not-probed"
			if config.ReadinessKey != "" {
				if err := app.ProbeStorage(check, storageConfig(config.Evidence), config.ReadinessKey); err != nil {
					jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"service": role, "status": "scoped-storage-unavailable"})
					return
				}
				storage = "scoped-object-accessible"
			}
		}
		jsonResponse(w, http.StatusOK, map[string]string{
			"service": role, "status": "ready", "database": "reachable", "storage": storage,
			"pipeline": "inspect-job-state-separately",
		})
	})
	if role == "core" {
		mux.Handle("/api/", runtime.api)
		mux.Handle("/api", runtime.api)
		mux.Handle("/", ui)
	}
	server := &http.Server{
		Addr: config.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute,
		TLSConfig: tlsConfig,
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return fmt.Errorf("listen for %s failed", role)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var workers sync.WaitGroup
	workerErrors := make(chan error, len(runtime.workers))
	for _, worker := range runtime.workers {
		workers.Add(1)
		go func(run func(context.Context) error) {
			defer workers.Done()
			if err := run(runCtx); err != nil {
				workerErrors <- err
			}
		}(worker)
	}
	workerDone := make(chan struct{})
	go func() { workers.Wait(); close(workerDone) }()
	exited := make(chan error, 1)
	go func() {
		if config.TLSCertFile != "" {
			exited <- server.ServeTLS(listener, "", "")
		} else {
			exited <- server.Serve(listener)
		}
	}()
	slog.Info("service listening", "role", role, "address", listener.Addr().String())
	var serveError, workerError error
	select {
	case serveError = <-exited:
	case workerError = <-workerErrors:
	case <-ctx.Done():
	}
	stop()
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	select {
	case <-workerDone:
	case <-shutdown.Done():
		return errors.Join(shutdownErr, errors.New("worker shutdown timed out"))
	}
	if serveError != nil && !errors.Is(serveError, http.ErrServerClosed) {
		return errors.Join(shutdownErr, errors.New("service HTTP listener stopped unexpectedly"))
	}
	return errors.Join(shutdownErr, workerError)
}

func drain(ctx context.Context, kind string, process func(context.Context) error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := process(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("processing interrupted", "kind", kind, "errorType", fmt.Sprintf("%T", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func work(ctx context.Context, queue *jobs.Store, storage *evidence.Reader, config Config) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			workspaces := []string{config.Workspace}
			var err error
			if config.Workspace == "" {
				workspaces, err = queue.PendingWorkspaces(ctx, 100)
			}
			if err != nil {
				slog.Warn("job discovery unavailable", "errorType", fmt.Sprintf("%T", err))
				continue
			}
			for _, workspace := range workspaces {
				if ctx.Err() != nil {
					return
				}
				report, err := processIntegrity(ctx, queue, storage, jobs.Claim{
					WorkspaceID: workspace, WorkerID: config.WorkerID, LeaseFor: config.Jobs.MaxLease,
				})
				if err != nil {
					slog.Warn("job processing interrupted", "job", report.JobID, "errorType", fmt.Sprintf("%T", err))
					continue
				}
				if report.Processed {
					slog.Info("job processed", "job", report.JobID, "state", report.State, "failureCode", report.FailureCode)
				}
			}
		}
	}
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("response write failed", "errorType", strings.TrimPrefix(fmt.Sprintf("%T", err), "*"))
	}
}
