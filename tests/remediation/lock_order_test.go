//go:build integration

package remediation

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type lockOperationKey struct{}
type lockQueryKey struct{}

type lockEvent struct {
	Phase         string `json:"phase"`
	PID           uint32 `json:"pid"`
	InTransaction bool   `json:"inTransaction"`
	Rows          int64  `json:"rows,omitempty"`
	QueryFailed   bool   `json:"queryFailed,omitempty"`
}

type lockOperation struct {
	mu                  sync.Mutex
	insert              bool
	cancel              context.CancelFunc
	protected           bool
	protectedPID        uint32
	membershipCompleted bool
	events              []lockEvent
	workspaceReady      chan uint32
	workspaceAttempt    chan uint32
	unsafe              chan struct{}
	release             chan struct{}
	releaseOnce         sync.Once
}

func newLockOperation(insert bool, cancel context.CancelFunc) *lockOperation {
	return &lockOperation{insert: insert, cancel: cancel, workspaceReady: make(chan uint32, 1),
		workspaceAttempt: make(chan uint32, 1), unsafe: make(chan struct{}, 1), release: make(chan struct{})}
}

func (o *lockOperation) releaseHold() { o.releaseOnce.Do(func() { close(o.release) }) }

func (o *lockOperation) snapshot() ([]lockEvent, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]lockEvent(nil), o.events...), o.membershipCompleted
}

type lockQuery struct {
	operation *lockOperation
	kind      string
	pid       uint32
}

type lockOrderTracer struct{ workspaceTable, membershipTable string }

func (tracer *lockOrderTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	operation, _ := ctx.Value(lockOperationKey{}).(*lockOperation)
	if operation == nil {
		return ctx
	}
	sql := strings.ToUpper(strings.Join(strings.Fields(data.SQL), " "))
	pid := conn.PgConn().PID()
	if sql == "BEGIN" || strings.HasPrefix(sql, "BEGIN ") || sql == "COMMIT" || sql == "ROLLBACK" {
		operation.mu.Lock()
		operation.protected = false
		operation.mu.Unlock()
	}
	locking := strings.Contains(sql, "FOR KEY SHARE") || strings.Contains(sql, "FOR SHARE") ||
		strings.Contains(sql, "FOR UPDATE") || strings.Contains(sql, "FOR NO KEY UPDATE")
	kind := ""
	if locking && strings.Contains(sql, strings.ToUpper(tracer.workspaceTable)) {
		kind = "workspace"
		select {
		case operation.workspaceAttempt <- pid:
		default:
		}
	} else if locking && strings.Contains(sql, strings.ToUpper(tracer.membershipTable)) {
		kind = "membership"
	}
	if kind == "" {
		return ctx
	}
	inTransaction := conn.PgConn().TxStatus() == 'T'
	operation.mu.Lock()
	operation.events = append(operation.events, lockEvent{Phase: kind + "-start", PID: pid, InTransaction: inTransaction})
	safeOrder := operation.protected && operation.protectedPID == pid && inTransaction
	operation.mu.Unlock()
	if operation.insert && kind == "membership" && !safeOrder {
		// Cancel before this membership-lock query executes; never stage the unsafe competing writer.
		select {
		case operation.unsafe <- struct{}{}:
		default:
		}
		operation.cancel()
	}
	return context.WithValue(ctx, lockQueryKey{}, lockQuery{operation: operation, kind: kind, pid: pid})
}

func (*lockOrderTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	query, ok := ctx.Value(lockQueryKey{}).(lockQuery)
	if !ok {
		return
	}
	operation := query.operation
	inTransaction := conn.PgConn().TxStatus() == 'T'
	acquired := data.Err == nil && data.CommandTag.RowsAffected() == 1 && inTransaction
	operation.mu.Lock()
	operation.events = append(operation.events, lockEvent{Phase: query.kind + "-end", PID: query.pid,
		InTransaction: inTransaction, Rows: data.CommandTag.RowsAffected(), QueryFailed: data.Err != nil})
	if query.kind == "workspace" && acquired {
		operation.protected, operation.protectedPID = true, query.pid
	}
	if query.kind == "membership" && acquired && operation.protected && operation.protectedPID == query.pid {
		operation.membershipCompleted = true
	}
	operation.mu.Unlock()
	if operation.insert && query.kind == "workspace" && acquired {
		select {
		case operation.workspaceReady <- query.pid:
		default:
		}
		// This scheduling hold changes no SQL/result/state and ends on release or owned cancellation.
		select {
		case <-operation.release:
		case <-ctx.Done():
		}
	}
}

type observedAPIResult struct {
	Status int
	Body   []byte
}

func startObservedAPI(h *harness, ctx context.Context, operation *lockOperation, method, route string, body object) <-chan observedAPIResult {
	h.t.Helper()
	check(h.t, h.apiCalls.Add(1) <= 160, "existing bounded API request budget exceeded")
	ctx = context.WithValue(ctx, lockOperationKey{}, operation)
	request := httptest.NewRequest(method, h.cfg.PublicOrigin+route, bytes.NewReader(encode(h.t, body))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.cfg.PublicOrigin)
	request.Header.Set("X-ASPM-Workspace-ID", h.admin.Workspace)
	request.AddCookie(h.admin.Cookie)
	done := make(chan observedAPIResult, 1)
	go func() {
		response := httptest.NewRecorder()
		h.app.Handler.ServeHTTP(response, request)
		done <- observedAPIResult{Status: response.Code, Body: bytes.Clone(response.Body.Bytes())}
	}()
	return done
}

func finishObservedAPI(t *testing.T, done <-chan observedAPIResult) observedAPIResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(4 * time.Second):
		t.Fatal("owned request did not finish after bounded cancellation/release")
		return observedAPIResult{}
	}
}

func actualWorkspaceWait(h *harness, holder, waiter uint32) bool {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for attempts := 0; attempts < 25; attempts++ {
		var blocked bool
		err := h.db.QueryRow(ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer))`,
			int32(holder), int32(waiter)).Scan(&blocked)
		if err == nil && blocked {
			return true
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			must(h.t, "read only owned backend blocker metadata", err)
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
	return false
}

func TestRemediationInsertionWorkspaceBeforeMembershipA2(t *testing.T) {
	for _, variant := range []string{"connection-create", "delivery-enqueue"} {
		t.Run(variant, func(t *testing.T) {
			h := newHarness(t, true)
			route, expected := connectionsPath, 201
			token := secret(t)
			h.addSecret(token)
			body := object{"profile": slackProfile, "name": "Owned lock-order destination", "channel": "C123", "token": token, "enabled": true}
			if variant == "delivery-enqueue" {
				finding := h.importFinding(h.admin, "Owned insertion lock-order source")
				connection := h.createConnection(h.admin, "Owned existing destination", "C123", token, true)
				route, expected = deliveriesPath(finding.ID), 202
				body = object{"connectionId": connection.ID, "idempotencyKey": "owned-lock-order"}
			}
			tracer := &lockOrderTracer{workspaceTable: h.table("workspaces"), membershipTable: h.table("memberships")}
			h.cfg.Database.MaxConnections = 3
			h.cfg.QueryTracer = tracer
			h.reopen()
			insertCtx, cancelInsert := context.WithTimeout(h.ctx, 3*time.Second)
			insertion := newLockOperation(true, cancelInsert)
			defer cancelInsert()
			defer insertion.releaseHold()
			insertDone := startObservedAPI(h, insertCtx, insertion, "POST", route, body)
			var holder uint32
			unsafeOrder, competingUpdateStarted, observedPGWait := false, false, false
			var insertionResult, updateResult observedAPIResult
			defer func() {
				trace, membershipCompleted := insertion.snapshot()
				observation := struct {
					Variant, OwnedSchema                  string
					UnsafeOrderCancelled                  bool
					CompetingSameRoleUpdateStarted        bool
					ActualPGBlockerObserved               bool
					MembershipCompletedAfterWorkspace     bool
					InsertionHTTPStatus, UpdateHTTPStatus int
					Trace                                 []lockEvent
					LiveDeadlockReproduced                bool
				}{variant, h.cfg.Database.Schema, unsafeOrder, competingUpdateStarted, observedPGWait,
					membershipCompleted, insertionResult.Status, updateResult.Status, trace, false}
				target := filepath.Join(required(t, "ASPM_REMEDIATION_ARTIFACT_DIR"), "a2-lock-order-"+variant+"-"+nonce(t)+".json")
				must(t, "record sanitized runtime locking observation", os.WriteFile(target, encode(t, observation), 0600))
				t.Log("A2 read-only locking observation:", target)
			}()
			select {
			case <-insertion.unsafe:
				unsafeOrder = true
				cancelInsert()
				insertion.releaseHold()
				insertionResult = finishObservedAPI(t, insertDone)
				h.noSecrets(insertionResult.Body)
				t.Error("runtime membership lock was attempted before completed workspace-row protection; owned request canceled before locking and competing update NOT started")
				return
			case holder = <-insertion.workspaceReady:
			case <-insertCtx.Done():
				cancelInsert()
				insertion.releaseHold()
				insertionResult = finishObservedAPI(t, insertDone)
				t.Error("actual workspace lock acquisition was not observed within the owned request bound")
				return
			}

			updateCtx, cancelUpdate := context.WithTimeout(h.ctx, 3*time.Second)
			defer cancelUpdate()
			update := newLockOperation(false, cancelUpdate)
			competingUpdateStarted = true
			updateDone := startObservedAPI(h, updateCtx, update, "PATCH", "/api/v1/users/"+h.admin.User.ID, object{"role": "admin"})
			var waiter uint32
			select {
			case waiter = <-update.workspaceAttempt:
			case <-updateCtx.Done():
				cancelInsert()
				cancelUpdate()
				insertion.releaseHold()
				insertionResult, updateResult = finishObservedAPI(t, insertDone), finishObservedAPI(t, updateDone)
				t.Error("benign same-role update did not reach its actual workspace-lock query")
				return
			}
			observedPGWait = holder != waiter && actualWorkspaceWait(h, holder, waiter)
			insertion.releaseHold()
			if !observedPGWait {
				cancelInsert()
				cancelUpdate()
			}
			insertionResult, updateResult = finishObservedAPI(t, insertDone), finishObservedAPI(t, updateDone)
			h.noSecrets(insertionResult.Body)
			h.noSecrets(updateResult.Body)
			if !observedPGWait {
				t.Error("real PG did not show the same-role workspace writer waiting on the insertion's acquired protection")
				return
			}
			_, membershipCompleted := insertion.snapshot()
			check(t, membershipCompleted && insertionResult.Status == expected && updateResult.Status == 200,
				"ordered real insertion and same-role update did not both complete normally")
			session := h.json(h.admin, "GET", "/api/v1/session", nil, 200)
			adminRetained := false
			for _, workspace := range session.Workspaces {
				adminRetained = adminRetained || workspace.ID == h.admin.Workspace && workspace.Role == "admin"
			}
			check(t, adminRetained, "the concurrency probe must leave admin membership unchanged")
		})
	}
}
