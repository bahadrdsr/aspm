//go:build integration && teams_runtime

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/service"
)

const teamsOriginsEnvironment = "ASPM_TEAMS_WORKFLOW_ORIGINS"

func teamsRuntimeSettings(t *testing.T, database, schema, address string, key []byte, slack, ca string, jira, teams []string) map[string]string {
	t.Helper()
	values := runtimeSettings(t, database, schema, address, key, slack, ca, jira)
	if teams != nil {
		values[teamsOriginsEnvironment] = string(encoded(t, teams))
	}
	return values
}

func teamsRuntimeConfig(t *testing.T, h *harness, database, slack, ca string, jira, teams []string) service.Config {
	t.Helper()
	runtimeEnvironment(t, teamsRuntimeSettings(t, database, h.cfg.Schema, runtimeFreeAddress(t),
		h.cfg.IntegrationEncryptionKey, slack, ca, jira, teams))
	config, err := service.Environment("delivery")
	must(t, "construct actual Teams delivery environment", err)
	runtimeClientPolicy(t, config.DeliveryClient)
	t.Cleanup(config.DeliveryClient.CloseIdleConnections)
	return config
}

func teamsRuntimeOrigins(t *testing.T, config *service.Config) reflect.Value {
	t.Helper()
	field := reflect.ValueOf(config).Elem().FieldByName("TeamsWorkflowOrigins")
	check(t, field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf([]string{}),
		"TeamsWorkflowOrigins []string must retain the explicit Environment selection")
	return field
}

func teamsRuntimeOwnSnapshot(t *testing.T, config *service.Config) func() {
	t.Helper()
	prior := runtimeOwnSnapshot(t, config)
	key := bytes.Clone(config.IntegrationEncryptionKey[:cap(config.IntegrationEncryptionKey)])
	protos := config.DeliveryClient.Transport.(*http.Transport).TLSClientConfig.NextProtos
	protocols := encoded(t, protos[:cap(protos)])
	field := reflect.ValueOf(config).Elem().FieldByName("TeamsWorkflowOrigins")
	var backing []byte
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf([]string{}) {
		backing = encoded(t, field.Slice(0, field.Cap()).Interface())
	}
	return func() {
		t.Helper()
		prior.unchanged(t, config)
		actual := teamsRuntimeOrigins(t, config)
		protos := config.DeliveryClient.Transport.(*http.Transport).TLSClientConfig.NextProtos
		check(t, bytes.Equal(key, config.IntegrationEncryptionKey[:cap(config.IntegrationEncryptionKey)]) &&
			bytes.Equal(protocols, encoded(t, protos[:cap(protos)])) &&
			bytes.Equal(backing, encoded(t, actual.Slice(0, actual.Cap()).Interface())),
			"Run mutated caller Teams/TLS/key backing storage")
	}
}

type teamsRuntimeRoutes struct {
	teams  *runtimeRelay
	common runtimeRoutes
	exact  atomic.Int32
}

func teamsRuntimeObserve(t *testing.T, ctx context.Context, n *teamsServer, label string) *teamsRuntimeRoutes {
	t.Helper()
	r := &teamsRuntimeRoutes{
		teams: newRuntimeRelay(t, ctx, label+"-teams", strings.TrimPrefix(n.server.URL, "https://"), nil),
		common: runtimeObserveNative(t, ctx, n.common, label),
	}
	originalURL, originalHandler := n.server.URL, n.server.Config.Handler
	n.server.URL = r.teams.origin()
	// Observe the full signed target, then delegate every response to the frozen native fixture.
	n.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		n.mu.Lock()
		p := n.plan
		n.mu.Unlock()
		_, authorization := request.Header["Authorization"]
		_, proxyAuthorization := request.Header["Proxy-Authorization"]
		if p != nil && "https://"+request.Host+request.RequestURI == p.url &&
			request.Method == "POST" && request.TLS != nil && request.TLS.Version >= tls.VersionTLS12 &&
			!authorization && !proxyAuthorization && request.Header.Get("Cookie") == "" {
			r.exact.Add(1)
		} else {
			t.Error("native Teams signed-target, TLS or no-ambient-credential boundary changed")
		}
		originalHandler.ServeHTTP(w, request)
	})
	t.Cleanup(func() {
		r.close()
		n.server.URL = originalURL
		check(t, r.exact.Load() == n.calls.Load(), "native signed-target observer missed a Teams request")
		runtimeEvidence(t, label+"-teams-native", object{
			"nativePOST": n.calls.Load(), "fullSignedTargetMatches": r.exact.Load(),
			"observedBoundary": "exact HTTPS host plus escaped path plus complete ordered query; no Authorization/cookie",
			"secretTargetRecorded": false,
		})
	})
	return r
}

func (r *teamsRuntimeRoutes) close() {
	r.teams.close()
	r.common.close()
}

func teamsRuntimeStartCommand(h *harness, database, slack, ca string, jira, teams []string, label string) *runtimeRole {
	t := h.t
	t.Helper()
	binaryPath, wantHash := os.Getenv("ASPM_JIRA_COMMAND_BINARY"), os.Getenv("ASPM_JIRA_COMMAND_SHA256")
	output := os.Getenv("ASPM_JIRA_COMMAND_ARTIFACT_DIR")
	check(t, filepath.IsAbs(binaryPath) && filepath.Dir(binaryPath) == output &&
		strings.HasPrefix(filepath.Base(output), "teams-") && len(wantHash) == 64,
		"BLOCKED: actual main/hash must belong to the fresh feature-specific reported output root")
	info, err := os.Stat(binaryPath)
	must(t, "locate newly built actual delivery main", err)
	check(t, info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 128<<20,
		"actual command is not a bounded regular binary")
	data, err := os.ReadFile(binaryPath)
	must(t, "hash actual delivery command", err)
	check(t, digest(data) == wantHash, "actual main differs from this invocation's recorded mainbuild")
	r := &runtimeRole{h: h, label: label, address: runtimeFreeAddress(t),
		mode: "actual-cmd/delivery-worker", done: make(chan struct{})}
	values := teamsRuntimeSettings(t, database, h.cfg.Schema, r.address, h.cfg.IntegrationEncryptionKey, slack, ca, jira, teams)
	env, names := runtimeChildEnvironment(values)
	command := exec.Command(binaryPath)
	command.Env, command.Stdout, command.Stderr, command.Dir = env, &h.log, &h.log, output
	began := time.Now().UTC()
	must(t, "start actual delivery main with explicit Teams selection", command.Start())
	stopOnDeadline := context.AfterFunc(h.ctx, func() { _ = command.Process.Kill() })
	r.client = directClient(t, "http://"+r.address)
	r.client.Timeout = time.Second
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			stopOnDeadline()
			_ = command.Process.Kill()
			exited := false
			select {
			case <-r.done:
				exited = true
			case <-time.After(5 * time.Second):
				t.Error("owned Teams actual-main PID did not exit within its cleanup bound")
			}
			h.private(h.log.data())
			runtimeEvidence(t, label+"-role", object{
				"mode": r.mode, "binarySHA256": wantHash, "pid": command.Process.Pid,
				"startedAt": began, "finishedAt": time.Now().UTC(), "environmentNames": names,
				"health": r.health, "ownedPIDExited": exited, "privateProcessLogChecked": true,
				"cleanup": "owned PID kill; not graceful OS-signal evidence",
			})
		})
	}
	t.Cleanup(r.stop)
	go func() { r.err = command.Wait(); close(r.done) }()
	runtimeEvidence(t, label+"-process", object{
		"pid": command.Process.Pid, "binaryPath": binaryPath, "binarySHA256": wantHash,
		"startedNotBeforeUnixMs": began.UnixMilli(), "startedNotAfterUnixMs": time.Now().UnixMilli(),
	})
	r.ready()
	return r
}

func teamsRuntimeAwaitTerminal(h *harness, who actor, id string) teamsDelivery {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 6*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for reads := 0; reads < 160; reads++ {
		var state string
		var noReceipt bool
		var storedFailure []byte
		var started, attempted, completed *time.Time
		must(h.t, "observe scoped Teams runtime outcome", h.db.QueryRow(ctx,
			"SELECT state,receipt IS NULL OR receipt='null'::jsonb,failure,dispatch_started_at,create_attempted_at,completed_at FROM "+
				h.table("finding_deliveries")+" WHERE workspace_id=$1 AND id=$2", who.Workspace, id).
			Scan(&state, &noReceipt, &storedFailure, &started, &attempted, &completed))
		if state != "queued" && state != "dispatching" {
			actual := h.teamsDelivery(who, id)
			var nativeFailure *failure
			if len(storedFailure) != 0 {
				nativeFailure = decoded[*failure](h.t, storedFailure)
			}
			check(h.t, actual.State == state && noReceipt && actual.Receipt == nil &&
				runtimeSameTime(actual.DispatchStartedAt, started) &&
				runtimeSameTime(actual.OutboundAttemptedAt, attempted) &&
				runtimeSameTime(actual.CompletedAt, completed) && reflect.DeepEqual(actual.Failure, nativeFailure),
				"Teams API public outbound marker or null receipt differs from its scoped committed PG record")
			runtimeEvidence(h.t, "teams-durable-outcome", object{
				"intentID": id, "profile": actual.Profile, "state": state, "failure": actual.Failure,
				"outboundAttemptedAt": attempted, "completedAt": completed, "receipt": nil,
				"scopedPGMatchesAPI": true,
			})
			return actual
		}
		select {
		case <-ctx.Done():
			h.t.Fatal("Teams runtime outcome exceeded its context bound")
		case <-ticker.C:
		}
	}
	h.t.Fatal("Teams runtime observation count exceeded its bound")
	return teamsDelivery{}
}

func teamsRuntimeBinding(t *testing.T, queued, done teamsDelivery) {
	t.Helper()
	runtimeSameBinding(t, queued.delivery, done.delivery)
	same(t, "runtime rewrote approved Teams public destination", done.Destination, queued.Destination)
	check(t, done.Profile == teamsProfile && done.Channel == "" && done.Jira == nil &&
		done.CreatedAt.Equal(queued.CreatedAt) && done.CreateAttemptedAt == nil && done.Receipt == nil && done.DispatchStartedAt != nil &&
		done.OutboundAttemptedAt != nil && done.CompletedAt != nil &&
		!done.OutboundAttemptedAt.Before(*done.DispatchStartedAt) &&
		!done.CompletedAt.Before(*done.OutboundAttemptedAt),
		"Teams runtime invented a channel/receipt/Jira field or lost committed outbound ordering")
}

func teamsRuntimeAccepted(t *testing.T, queued, done teamsDelivery) {
	t.Helper()
	teamsRuntimeBinding(t, queued, done)
	check(t, done.State == "accepted" && done.Failure == nil,
		"RED: approved Teams origin did not reach accepted through actual delivery wiring")
}

func teamsRuntimeRefused(t *testing.T, queued, done teamsDelivery, redirect bool) {
	t.Helper()
	teamsRuntimeBinding(t, queued, done)
	wantState, wantCode, wantStatus := "uncertain", "uncertain", 0
	if redirect {
		wantState, wantCode, wantStatus = "blocked", "scope", 307
	}
	f := done.Failure
	check(t, done.State == wantState && f != nil && f.Code == wantCode && f.HTTPStatus == wantStatus &&
		f.NativeCode == "" && f.Stage == "" && len(f.MissingFields) == 0 &&
		!f.Retryable && f.RetryAfterSeconds == 0,
		"Teams refusal changed published safe semantics or confused a pre-send marker with an actual socket/receipt")
}

func teamsRuntimeHistory(h *harness, who actor, findingID string, expected ...teamsDelivery) {
	h.t.Helper()
	history := h.json(who, "GET", historyPath(findingID)+"?profile="+teamsProfile, nil, 200)
	check(h.t, len(history.Items) == len(expected) && history.Total == len(expected) && history.NextCursor == nil,
		"public Teams history omitted or invented actual-main intents")
	wanted := make(map[string]teamsDelivery, len(expected))
	for _, value := range expected {
		wanted[value.ID] = value
	}
	for _, raw := range history.Items {
		actual := decoded[teamsDelivery](h.t, raw)
		value, present := wanted[actual.ID]
		check(h.t, present, "Teams history crossed the approved intent boundary")
		same(h.t, "history differs from actual committed Teams outcome", actual, value)
		item := decoded[object](h.t, raw)
		_, jiraMarker := item["createAttemptedAt"]
		_, outbound := item["outboundAttemptedAt"]
		check(h.t, !jiraMarker && outbound && item["receipt"] == nil, "history exposed Jira marker or fabricated channel receipt")
		delete(wanted, actual.ID)
	}
	check(h.t, len(wanted) == 0, "Teams history repeated an intent")
}

func teamsRuntimeOriginError(t *testing.T, err error, input string, private ...string) {
	t.Helper()
	check(t, err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded),
		"RED: malformed ASPM_TEAMS_WORKFLOW_ORIGINS was ignored instead of rejected in preflight")
	message := err.Error()
	lower := strings.ToLower(message)
	check(t, (strings.Contains(lower, "aspm_teams_workflow_origins") || strings.Contains(lower, "teamsworkfloworigins")) &&
		!strings.Contains(lower, "aspm_jira_api_origins") && !strings.Contains(lower, "jiraapiorigins"),
		"Teams origin preflight must identify its field before DB/listener errors")
	check(t, len(message) <= 512 && !strings.Contains(message, "private-origin-canary"),
		"Teams origin preflight echoed private input or an unbounded diagnostic")
	if len(input) > 5 {
		check(t, !strings.Contains(message, input), "Teams preflight echoed the raw origin setting")
	}
	for _, value := range private {
		check(t, value == "" || !strings.Contains(message, value), "Teams preflight exposed unrelated private input")
	}
}
