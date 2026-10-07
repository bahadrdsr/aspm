//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/jackc/pgx/v5"
)

func adoAsyncStep(ctx context.Context, worker *app.CollectionWorker) <-chan stepResult {
	done := make(chan stepResult, 1)
	go func() {
		processed, err := worker.ProcessNext(ctx)
		done <- stepResult{processed, err}
	}()
	return done
}

func (h *adoHarness) adoAsyncHTTP(who actor, path string, status int) <-chan []byte {
	done := make(chan []byte, 1)
	go func() {
		defer close(done)
		body, _ := h.request(h.ctx, who, "POST", path, adoBridgeInput(), status)
		done <- body
	}()
	return done
}

func adoHTTPDone(t *testing.T, done <-chan []byte) []byte {
	t.Helper()
	select {
	case body := <-done:
		check(t, len(body) > 0, "held bridge request did not produce its explicit denial")
		return body
	case <-time.After(3 * time.Second):
		t.Fatal("held bridge did not release HTTP/storage resources")
		return nil
	}
}

type adoSQLKey struct{}
type adoSQLGate struct {
	budget *queryBudget
	table  string
	gate   *adoGate
	live   atomic.Bool
}

func (g *adoSQLGate) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx = g.budget.TraceQueryStart(ctx, conn, data)
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.HasPrefix(sql, "insert ") && strings.Contains(sql, strings.ToLower(g.table)) {
		return context.WithValue(ctx, adoSQLKey{}, true)
	}
	return ctx
}
func (g *adoSQLGate) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	marked, _ := ctx.Value(adoSQLKey{}).(bool)
	if marked && data.Err == nil && data.CommandTag.RowsAffected() == 1 {
		g.live.Store(conn.PgConn().TxStatus() == 'T')
		g.gate.wait(ctx)
	}
}

func TestADOA4IndependentAuthorityIntegrityAndBounds(t *testing.T) {
	t.Run("tampered-stored-selection", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
		job := h.adoQueue(h.admin, source.ID, selection, "explicit-binding", 202)
		changed, err := h.db.Exec(h.ctx, "UPDATE "+h.table("source_collections")+
			" SET azure_devops_selection=jsonb_set(azure_devops_selection,'{buildId}',to_jsonb($3::text))"+
			" WHERE workspace_id=$1 AND id=$2 AND state='queued'", h.admin.Workspace, job.ID, "82")
		must(t, "tamper only the owned API-created queued selection without changing its binding", err)
		check(t, changed.RowsAffected() == 1, "tamper probe changed other than its one existing owned intent")
		adoStep(t, h.ctx, h.adoWorker(h.adoWorkerConfig()), true)
		blocked := h.adoJob(h.admin, job.ID)
		check(t, blocked.State == "blocked" && blocked.Failure != nil && blocked.Failure.Code == "binding-conflict" &&
			blocked.AssetID == nil && blocked.RecordCount == 0 && h.native.calls.Load() == 0,
			"modified durable selection bypassed its immutable actor/target/build binding")
	})

	t.Run("pre-dispatch-current-authority", func(t *testing.T) {
		h := newADO(t, true)
		writer := h.user("analyst")
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		worker := h.adoWorker(h.adoWorkerConfig())
		for _, reason := range []string{"role", "disable", "rotate"} {
			source := h.adoSource(h.admin, target, token)
			job := h.adoQueue(writer, source.ID, selection, "before-"+reason, 202)
			code := "authorization-revoked"
			switch reason {
			case "role":
				h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "viewer"}, 200)
			case "disable":
				code = "connection-disabled"
				h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"enabled": false}, 200)
			case "rotate":
				code = "connection-changed"
				replacement := secret(t)
				h.remember(replacement)
				h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"token": replacement}, 200)
			}
			adoStep(t, h.ctx, worker, true)
			blocked := h.adoJob(h.admin, job.ID)
			check(t, blocked.State == "blocked" && blocked.Failure != nil && blocked.Failure.Code == code &&
				blocked.RecordCount == 0 && blocked.AssetID == nil && h.native.calls.Load() == 0 && h.publisher.puts.Load() == 0,
				"worker used stale connection/actor authority before its first native request")
			if reason == "role" {
				h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "analyst"}, 200)
			}
		}
	})

	for _, phase := range []string{"native-role", "publication-disabled"} {
		t.Run("held-"+phase, func(t *testing.T) {
			h := newADO(t, true)
			writer := h.user("analyst")
			target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
			source := h.adoSource(h.admin, target, token)
			hold := "report"
			if phase == "publication-disabled" {
				hold = ""
			}
			script := h.native.arm(target, selection, token, adoSARIF(false, false), "", hold)
			gate := script.gate
			if phase == "publication-disabled" {
				gate = h.publisher.hold(t, "PUT")
			}
			job := h.adoQueue(writer, source.ID, selection, "held", 202)
			worker := h.adoWorker(h.adoWorkerConfig())
			done := adoAsyncStep(h.ctx, worker)
			adoAwait(t, gate.arrived, "native/publication hold")
			probe, cancel := context.WithTimeout(h.ctx, 750*time.Millisecond)
			must(t, "native/S3 wait retained the single worker SQL slot", worker.Ping(probe))
			h.request(probe, h.admin, "GET", "/api/v1/assets", nil, 200)
			cancel()
			wantCode := "authorization-revoked"
			if phase == "native-role" {
				h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "viewer"}, 200)
			} else {
				h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"enabled": false}, 200)
				wantCode = "connection-disabled"
			}
			adoAwait(t, gate.cancelled, "active current-authority cancellation")
			result := awaitStep(t, done)
			if result.err != nil {
				h.private([]byte(result.err.Error()))
			}
			blocked := h.adoJob(h.admin, job.ID)
			check(t, blocked.State == "blocked" && blocked.Failure != nil && blocked.Failure.Code == wantCode &&
				blocked.RecordCount == 0 && blocked.AssetID == nil && !blocked.Complete,
				"revoked old authority committed an asset or accepted collection records")
			gate.allow()
			h.reopen()
			before := h.native.calls.Load()
			adoStep(t, h.ctx, worker, false)
			same(t, "reopen resurrected terminal collection", h.adoJob(h.admin, job.ID), blocked)
			check(t, before == h.native.calls.Load(), "revoked terminal collection retried native reads")
		})
	}

	for _, reason := range []string{"read-role", "write-source", "write-asset"} {
		t.Run("held-bridge-"+reason, func(t *testing.T) {
			h := newADO(t, true)
			writer := h.user("analyst")
			target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
			source := h.adoSource(h.admin, target, token)
			job, report := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(false, false), "selected")
			status, method, tap := 403, "GET", h.reader
			if reason != "read-role" {
				status, method, tap = 409, "PUT", h.raw
			}
			if reason == "write-asset" {
				status = 404
			}
			gate := tap.hold(t, method)
			done := h.adoAsyncHTTP(writer, adoBridgePath(job.ID, report.ID), status)
			adoAwait(t, gate.arrived, "actual bridge storage boundary")
			probe, cancel := context.WithTimeout(h.ctx, 750*time.Millisecond)
			h.request(probe, h.admin, "GET", "/api/v1/assets", nil, 200)
			h.request(probe, h.admin, "GET", adoSources+"/"+source.ID, nil, 200)
			cancel()
			switch reason {
			case "read-role":
				h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "viewer"}, 200)
			case "write-source":
				h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"enabled": false}, 200)
			case "write-asset":
				h.request(h.ctx, h.admin, "DELETE", "/api/v1/assets/"+*job.AssetID, nil, 204)
			}
			adoAwait(t, gate.cancelled, "bridge source/session/asset recheck cancellation")
			body := adoHTTPDone(t, done)
			check(t, !bytes.Contains(body, []byte(adoReportCanary)), "denied bridge exposed report bytes")
			gate.allow()
			same(t, "revoked bridge published an accepted intake row", h.rows("SELECT id FROM "+h.table("imports")), []string{})
			check(t, h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "revoked bridge created canonical findings")
		})
	}

	t.Run("natural-finalization-lease-no-provisional-commit", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
		job := h.adoQueue(h.admin, source.ID, selection, "expiry", 202)
		config := h.adoWorkerConfig()
		config.LeaseDuration = 750 * time.Millisecond
		gate := &adoSQLGate{budget: &h.queries, table: h.table("assets"), gate: newADOGate(t)}
		config.Database.QueryTracer = gate
		worker := h.adoWorker(config)
		done := adoAsyncStep(h.ctx, worker)
		adoAwait(t, gate.gate.arrived, "actual provisional asset SQL")
		check(t, gate.live.Load(), "asset insert was not part of a real uncommitted transaction")
		check(t, h.json(h.admin, "GET", "/api/v1/assets", nil, 200).Total == 0, "provisional asset was visible before finalization")
		uncommitted := h.adoJob(h.admin, job.ID)
		check(t, uncommitted.AssetID == nil && uncommitted.RecordCount == 0, "collection records committed separately from the asset")
		var expired bool
		for i := 0; i < 80 && !expired; i++ {
			must(t, "observe actual PG clock lease expiry without modifying state", h.db.QueryRow(h.ctx,
				"SELECT lease_until<=clock_timestamp() FROM "+h.table("source_collections")+" WHERE id=$1", job.ID).Scan(&expired))
			if !expired {
				time.Sleep(25 * time.Millisecond)
			}
		}
		check(t, expired, "actual short collection lease did not expire")
		gate.gate.allow()
		result := awaitStep(t, done)
		if result.err != nil {
			h.private([]byte(result.err.Error()))
		}
		failed := h.adoJob(h.admin, job.ID)
		check(t, failed.State == "failed" && !failed.Complete && failed.RecordCount == 0 && failed.AssetID == nil,
			"expired finalizer committed asset/record state")
		check(t, h.json(h.admin, "GET", "/api/v1/assets", nil, 200).Total == 0, "expired transaction leaked its provisional asset")
		must(t, "close expired worker", worker.Close())
		before := h.native.calls.Load()
		adoStep(t, h.ctx, h.adoWorker(h.adoWorkerConfig()), false)
		check(t, h.native.calls.Load() == before, "fresh worker replayed an expired terminal collection")
	})

	for _, reason := range []string{"corrupt", "invalid-utf8", "upload-bound", "missing-collection-reader", "missing-collection-capability", "missing-raw-write"} {
		t.Run("bridge-"+reason, func(t *testing.T) {
			h := newADO(t, false)
			if reason == "upload-bound" {
				h.cfg.MaxUploadBytes = 1024
			}
			h.open()
			h.enroll()
			target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
			source := h.adoSource(h.admin, target, token)
			raw := adoSARIF(false, false)
			status := 503
			if reason == "invalid-utf8" {
				raw, status = append(raw, 0xff), 400
			}
			if reason == "upload-bound" {
				raw, status = append(raw, bytes.Repeat([]byte(" "), 1500)...), 413
			}
			job, report := h.adoCollectedReport(h.admin, source, selection, token, raw, "selected")
			if reason == "corrupt" {
				h.adoCorrupt(report.ID)
			}
			if reason == "missing-collection-reader" {
				h.cfg.CollectionStorage.AccessKey = h.capabilities.RawReader.AccessKey
				h.cfg.CollectionStorage.SecretKey = h.capabilities.RawReader.SecretKey
				h.reopen()
			}
			if reason == "missing-collection-capability" {
				h.cfg.CollectionStorage = nil
				h.reopen()
				h.adoQueue(h.admin, source.ID, selection, "missing-capability", 503)
			}
			if reason == "missing-raw-write" {
				h.cfg.Storage.AccessKey, h.cfg.Storage.SecretKey = h.capabilities.RawReader.AccessKey, h.capabilities.RawReader.SecretKey
				h.reopen()
			}
			body, _ := h.request(h.ctx, h.admin, "POST", adoBridgePath(job.ID, report.ID), adoBridgeInput(), status)
			check(t, !bytes.Contains(body, []byte(adoReportCanary)), "failed storage/intake leaked report contents")
			same(t, "invalid/unreadable report produced an acknowledged intake", h.rows("SELECT id FROM "+h.table("imports")), []string{})
			if reason == "missing-collection-reader" {
				check(t, h.reader.forbidden.Load() > 0 && h.raw.puts.Load() == 0, "missing read scope was not rejected by actual S3")
			}
			if reason == "missing-raw-write" {
				check(t, h.raw.forbidden.Load() > 0, "missing intake write scope was not rejected by actual S3")
			}
		})
	}

	t.Run("collection-role-cannot-publish-with-read-scope", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
		job := h.adoQueue(h.admin, source.ID, selection, "missing-write", 202)
		config := h.adoWorkerConfig()
		config.Storage.AccessKey, config.Storage.SecretKey = h.capabilities.CollectionReader.AccessKey, h.capabilities.CollectionReader.SecretKey
		worker := h.adoWorker(config)
		processed, err := worker.ProcessNext(h.ctx)
		check(t, processed, "collection with missing publisher scope was not claimed")
		if err != nil {
			h.private([]byte(err.Error()))
		}
		failed := h.adoJob(h.admin, job.ID)
		check(t, failed.State == "failed" && failed.RecordCount == 0 && failed.AssetID == nil &&
			h.publisher.forbidden.Load() > 0 && h.raw.calls.Load() == 0, "worker gained write/raw-intake authority or faked a collection")
	})

	t.Run("collection-publisher-has-no-raw-intake-read-scope", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		job, report := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(false, false), "selected")
		accepted := h.json(h.admin, "POST", adoBridgePath(job.ID, report.ID), adoBridgeInput(), 202).Import
		storage := h.cfg.Storage
		storage.AccessKey, storage.SecretKey = h.capabilities.CollectionPublisher.AccessKey, h.capabilities.CollectionPublisher.SecretKey
		worker, err := app.OpenImportWorker(h.ctx, app.ImportWorkerConfig{
			Database: h.adoDatabase("denied"), Storage: storage, MaxUploadBytes: h.cfg.MaxUploadBytes,
			NormalizedPrefix: h.upstream.Prefix + "normalized/",
		})
		must(t, "open actual worker with an intentionally insufficient existing role", err)
		defer worker.Close()
		must(t, "settle actual raw read permission failure", worker.ProcessImports(h.ctx))
		failed := h.json(h.admin, "GET", "/api/v1/imports/"+accepted.ID, nil, 200).Import
		check(t, failed.State == "failed" && failed.Failure != nil && h.raw.forbidden.Load() > 0 &&
			h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "collection publisher was granted raw-intake scope")
	})

	t.Run("queued-intake-current-requester-revoked", func(t *testing.T) {
		h := newADO(t, true)
		writer := h.user("analyst")
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		job, report := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(false, false), "selected")
		accepted := h.json(writer, "POST", adoBridgePath(job.ID, report.ID), adoBridgeInput(), 202).Import
		h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "viewer"}, 200)
		h.adoIngest()
		failed := h.json(h.admin, "GET", "/api/v1/imports/"+accepted.ID, nil, 200).Import
		check(t, failed.State == "failed" && failed.Failure != nil && failed.Failure.Code == "authorization-revoked" &&
			h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0,
			"independent ingestion used the collector's authority after the import requester was revoked")
	})

	t.Run("worker-constructor-capabilities", func(t *testing.T) {
		h := newADO(t, true)
		for _, reason := range []string{"key", "client", "insecure-tls", "http-origin"} {
			config := h.adoWorkerConfig()
			switch reason {
			case "key":
				config.EncryptionKey = nil
			case "client":
				config.Client = nil
			case "insecure-tls":
				config.Client = &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			case "http-origin":
				config.AzureDevOpsEndpoint = strings.Replace(config.AzureDevOpsEndpoint, "https:", "http:", 1)
			}
			worker, err := app.OpenCollectionWorker(h.ctx, config)
			if worker != nil {
				_ = worker.Close()
			}
			check(t, worker == nil && err != nil && h.native.calls.Load() == 0 && h.publisher.calls.Load() == 0,
				"worker accepted missing explicit key/verified native capability")
		}
	})
	t.Run("optional-core-key", func(t *testing.T) {
		h := newADO(t, false)
		h.cfg.IntegrationEncryptionKey = nil
		h.open()
		h.enroll()
		token := secret(t)
		h.remember(token)
		h.adoJSON(h.admin, "POST", adoSources, adoSourceInput(adoDefaultTarget(), token), 503)
		h.json(h.admin, "GET", "/api/v1/assets", nil, 200)
		check(t, h.native.calls.Load() == 0, "optional core key failure contacted native API")
	})

	type nativeFault struct {
		mode, code string
		calls, records int
	}
	for _, family := range []struct {
		name string
		faults []nativeFault
	}{
		{"permissions-repository", []nativeFault{{"code-read-denied", "auth", 1, 0}, {"build-read-denied", "auth", 2, 1}, {"wrong-repository", "scope", 1, 0}}},
		{"project-build", []nativeFault{{"wrong-build-project", "scope", 2, 1}, {"wrong-project", "scope", 3, 3}, {"wrong-build", "scope", 3, 3}}},
		{"download-scope", []nativeFault{{"wrong-org", "scope", 3, 3}, {"foreign-origin", "scope", 3, 3}, {"query-repeat", "scope", 3, 3}}},
		{"artifact-zip-path", []nativeFault{{"query-artifact", "scope", 3, 3}, {"zip-traversal", "scope", 4, 3}, {"zip-duplicate", "scope", 4, 3}}},
		{"zip-bounds", []nativeFault{{"zip-symlink", "scope", 4, 3}, {"zip-large", "limit", 4, 3}, {"zip-entries", "limit", 4, 3}}},
		{"transport-bounds", []nativeFault{{"request-cap", "limit", 3, 3}, {"download-large", "limit", 4, 3}, {"redirect", "scope", 4, 3}}},
	} {
		t.Run("native-"+family.name, func(t *testing.T) {
			h := newADO(t, true)
			target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
			source := h.adoSource(h.admin, target, token)
			for _, fault := range family.faults {
				before := h.native.calls.Load()
				h.native.arm(target, selection, token, adoSARIF(false, false), fault.mode, "")
				job := h.adoQueue(h.admin, source.ID, selection, "bounded-"+fault.mode, 202)
				config := h.adoWorkerConfig()
				if fault.mode == "request-cap" {
					config.Limits.Requests = 3
				}
				if fault.mode == "zip-entries" {
					config.Limits.Bytes = 1 << 20
				}
				worker := h.adoWorker(config)
				adoStep(t, h.ctx, worker, true)
				must(t, "close native boundary worker before the next bounded row", worker.Close())
				value := h.adoJob(h.admin, job.ID)
				wantState := "partial"
				if fault.records == 0 {
					wantState = "failed"
				}
				check(t, value.State == wantState && !value.Complete && value.RecordCount == fault.records &&
					value.Failure != nil && value.Failure.Code == fault.code && int(h.native.calls.Load()-before) == fault.calls,
					"native bound failure lost partial records, misclassified success or broadened requests")
				if fault.records > 0 {
					check(t, len(value.Gaps) > 0 && value.AssetID != nil, "valid prior repository/build/artifact context was discarded")
				} else {
					check(t, value.AssetID == nil, "invalid/unavailable repository created an accepted asset link")
				}
				t.Log("completed actual native boundary row:", fault.mode)
			}
			check(t, h.raw.puts.Load() == 0 && h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0,
				"native failure caused intake or canonical findings")
		})
	}
}
