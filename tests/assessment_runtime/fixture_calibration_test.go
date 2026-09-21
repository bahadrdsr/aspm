//go:build integration && assessment_runtime_fixture

package assessment_runtime

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func TestAssessmentRuntimeOwnedServiceIntakeAndNativeFixtureCalibration(t *testing.T) {
	f := newFixture(t)
	core := f.coreService("", f.key, true)
	finding := core.seed()
	n := newNative(f)
	p, pol, g, key := core.profile(n, false)
	core.json(core.admin, "POST", "/api/v1/findings/"+finding.ID+"/assessment-previews", map[string]any{
		"observationId": finding.Observations[0].ID, "profileId": p.ID, "grantId": g.ID, "context": reviewedText, "reviewed": true}, 503)
	check(t, n.calls.Load() == 0, "fixture-only empty-scope core unexpectedly dispatched")

	var backend *app.Application
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backend.Handler.ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	var err error
	backend, err = app.Open(f.ctx, app.Config{DatabaseURL: f.database.DatabaseURL, Schema: f.database.Schema, ApplicationName: "runtime-fixture-backend-" + nonce(t),
		MaxConnections: 2, Storage: app.StorageConfig{Endpoint: f.raw.Endpoint, Bucket: f.raw.Bucket, Prefix: f.raw.Prefix, Region: f.raw.Region, AccessKey: f.raw.AccessKey, SecretKey: f.raw.SecretKey, Timeout: 4 * time.Second},
		IntegrationEncryptionKey: f.key, AssessmentScope: f.scope, PublicOrigin: server.URL, ManualProcessing: true})
	must(t, "open already-accepted backend only for native fixture calibration", err)
	t.Cleanup(func() { must(t, "close fixture-only accepted backend", backend.Close()) })
	client := server.Client()
	client.Timeout = 2 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	api := &coreAPI{f: f, server: server, client: client, admin: core.admin}
	job := api.enqueue(finding, p, pol, g, "native-fixture-only")
	before, storage := f.domainSnapshot(), f.storeCalls.Load()
	script := n.arm(job, key, false)
	db := f.database
	db.MaxConnections = 1
	db.ApplicationName = "runtime-native-cal-" + nonce(t)
	config := app.AssessmentWorkerConfig{Database: db, EncryptionKey: f.key, WorkerID: "native-fixture-only", Scope: f.scope, Client: n.client,
		LeaseDuration: 15 * time.Second, AuthorizationInterval: 100 * time.Millisecond, RequestTimeout: 10 * time.Second, RequestWindow: time.Minute,
		MaxConcurrent: 1, RequestsPerWindow: 30, MaxInputBytes: 32768, MaxOutputTokens: 1024, MaxResponseBytes: 65536}
	must(t, "accepted pure worker validation", app.ValidateAssessmentWorkerConfig(config))
	worker, err := app.OpenAssessmentWorker(f.ctx, config)
	must(t, "open accepted backend for native boundary calibration only", err)
	t.Cleanup(func() { must(t, "close accepted calibration worker", worker.Close()) })
	worked, err := worker.ProcessNext(f.ctx)
	must(t, "actual accepted backend through owned native HTTP", err)
	check(t, worked, "native fixture-only backend did not process queued API state")
	event(t, f.ctx, script.ready, "A1-style protocol and actual marker checks before readiness")
	api.assertAdvisory(api.job(job.ID), job)
	check(t, n.calls.Load() == 1 && reflect.DeepEqual(before, f.domainSnapshot()) && f.storeCalls.Load() == storage, "native calibration repeated, mutated findings or fetched raw S3")
	stopRole(t, core.role)
	t.Log("Fixture-only PASS: actual existing core SERVICE/TLS/session/API intake and current empty-scope 503; accepted app backend/native adapter/committed-marker readiness/advisory exactness. New assessment SERVICE/Environment/compiled command forwarding was NOT executed or accepted.")
}
