//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/jackc/pgx/v5"
)

var m13VerificationWorkerID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type m13ProcessLog struct {
	mu       sync.Mutex
	data     bytes.Buffer
	overflow bool
}

func (l *m13ProcessLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const limit = 128 << 10
	before := l.data.Len()
	if l.data.Len() < limit {
		remaining := limit - l.data.Len()
		l.data.Write(data[:min(len(data), remaining)])
	}
	if before+len(data) > limit {
		l.overflow = true
	}
	return len(data), nil
}

func (l *m13ProcessLog) snapshot() ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.data.Bytes()), l.overflow
}

type m13VerificationCommand struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	address string
	client  *http.Client
	log     *m13ProcessLog
	private []string
	stopped bool
}

func m13FreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	ok(t, "reserve owned verification command address", err)
	address := listener.Addr().String()
	ok(t, "release owned verification command address", listener.Close())
	return address
}

func m13CommandEnvironment(t *testing.T, h *harness, address string) []string {
	t.Helper()
	var result []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			strings.HasPrefix(upper, "AZURE_") || strings.HasPrefix(upper, "OPENAI_") ||
			strings.HasPrefix(upper, "ANTHROPIC_") || strings.HasPrefix(upper, "GITHUB_") ||
			strings.HasPrefix(upper, "GH_") || strings.HasPrefix(upper, "SLACK_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		result = append(result, entry)
	}
	for name, value := range map[string]string{
		"ASPM_DATABASE_URL":                        h.services.cfg.DatabaseURL,
		"ASPM_SCHEMA":                              h.services.cfg.Schema,
		"ASPM_DB_MAX_CONNECTIONS":                  "2",
		"ASPM_LISTEN":                              address,
		"ASPM_VERIFICATION_LEASE_DURATION":         "1s",
		"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL": "25ms",
		"ASPM_VERIFICATION_MAX_FIXTURE_BYTES":      "65536",
	} {
		result = append(result, name+"="+value)
	}
	return result
}

func m13ReadJSON(t *testing.T, client *http.Client, address, path string, want int) map[string]string {
	t.Helper()
	response, err := client.Get("http://" + address + path)
	ok(t, "call actual verification command "+path, err)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("actual verification command %s status=%d, want=%d", path, response.StatusCode, want)
	}
	if want != http.StatusOK {
		return nil
	}
	var result map[string]string
	ok(t, "decode actual verification command "+path, json.NewDecoder(response.Body).Decode(&result))
	return result
}

func (c *m13VerificationCommand) requireSurfaces(t *testing.T) {
	t.Helper()
	if got := m13ReadJSON(t, c.client, c.address, "/healthz", http.StatusOK); !reflect.DeepEqual(got, map[string]string{
		"apiVersion": jobs.Version, "service": "verification", "status": "alive",
	}) {
		t.Fatalf("actual verification command health changed its existing bounded surface: %#v", got)
	}
	if got := m13ReadJSON(t, c.client, c.address, "/readyz", http.StatusOK); !reflect.DeepEqual(got, map[string]string{
		"service": "verification", "status": "ready", "database": "reachable",
		"schema": "compatible", "storage": "not-required", "pipeline": "inspect-job-state-separately",
	}) {
		t.Fatalf("actual verification command readiness made an unsafe or unavailable-system claim: %#v", got)
	}
	for _, path := range []string{"/", "/api", "/api/v1/session", "/metrics"} {
		m13ReadJSON(t, c.client, c.address, path, http.StatusNotFound)
	}
}

func startM13VerificationCommand(t *testing.T, h *harness) *m13VerificationCommand {
	t.Helper()
	binary := os.Getenv("ASPM_VERIFICATION_RUNTIME_BINARY")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("ASPM_VERIFICATION_RUNTIME_BINARY must select the actual compiled cmd/verification-worker executable")
	}
	info, err := os.Stat(binary)
	ok(t, "locate actual verification-worker executable", err)
	if !info.Mode().IsRegular() {
		t.Fatal("actual verification-worker executable is not a regular file")
	}
	address := m13FreeAddress(t)
	log := &m13ProcessLog{}
	command := exec.Command(binary)
	command.Env = m13CommandEnvironment(t, h, address)
	command.Stdout, command.Stderr = log, log
	ok(t, "start actual verification-worker command", command.Start())
	role := &m13VerificationCommand{
		command: command, done: make(chan struct{}), address: address, log: log,
		client: &http.Client{Transport: &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext,
		}, Timeout: 300 * time.Millisecond},
	}
	role.private = append(role.private, h.services.cfg.DatabaseURL)
	if database, parseErr := url.Parse(h.services.cfg.DatabaseURL); parseErr == nil && database.User != nil {
		role.private = append(role.private, database.User.String())
		if password, present := database.User.Password(); present {
			role.private = append(role.private, password)
		}
	}
	go func() {
		role.err = command.Wait()
		close(role.done)
	}()
	t.Cleanup(func() {
		role.kill(t)
		role.client.CloseIdleConnections()
	})
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-role.done:
			t.Fatalf("actual verification-worker exited before readiness (%T; output withheld)", role.err)
		case <-deadline.C:
			t.Fatal("actual verification-worker did not expose health/readiness within its startup bound")
		case <-ticker.C:
			response, requestErr := role.client.Get("http://" + address + "/readyz")
			if requestErr == nil {
				status := response.StatusCode
				response.Body.Close()
				if status == http.StatusOK {
					role.requireSurfaces(t)
					return role
				}
			}
		}
	}
}

func (c *m13VerificationCommand) kill(t *testing.T) {
	t.Helper()
	if c.stopped {
		return
	}
	c.stopped = true
	if c.command.ProcessState == nil {
		if err := c.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("terminate exact owned verification-worker PID failed (%T)", err)
		}
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("exact owned verification-worker PID did not terminate within its crash bound")
	}
	data, overflow := c.log.snapshot()
	if overflow {
		t.Fatal("verification-worker emitted more than 128 KiB during bounded acceptance")
	}
	for _, value := range c.private {
		if value != "" && bytes.Contains(data, []byte(value)) {
			t.Fatal("verification-worker output exposed private runtime input")
		}
	}
}

func waitM13VerificationRow(t *testing.T, h *harness, id string,
	accept func(verificationJobRow) bool, label string) verificationJobRow {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for {
		row := readVerificationJobRow(t, h, id)
		if accept(row) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; current state=%s attempts=%d fence=%d",
				label, row.State, row.Attempts, row.Fence)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitM13AdvisoryPublication(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var waiting int
		ok(t, "inspect owned verification publication wait", h.services.db.QueryRow(h.services.ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND usename=current_user
			  AND application_name='aspm-verification'
			  AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&waiting))
		if waiting == 1 {
			return
		}
		if waiting > 1 {
			t.Fatal("more than one verification publication waited on the single owned gate")
		}
		if time.Now().After(deadline) {
			t.Fatal("actual verification-worker never reached its fenced publication transaction")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestM13_VerificationCommandCrashRestartSettlesOnceAndPreservesTerminalState(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	h.clock.Store(time.Now().UTC().UnixNano())
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	_, _, finding := h.seed()
	findingBefore := h.finding(h.admin, finding.ID)
	storage.arm()
	suffix := "runtime-" + nonce(t)
	_, approval, queued := seedVerificationBinding(t, h, h.admin, finding, true, suffix)
	initial := readVerificationJobRow(t, h, queued.ID)
	if initial.State != "queued" || initial.Attempts != 0 || initial.Fence != 0 ||
		initial.WorkerID != nil || initial.CompletedAt != nil {
		t.Fatal("real API did not leave one ordinary durable queued V27 job before command startup")
	}

	databaseGate, err := pgx.Connect(h.services.ctx, h.services.cfg.DatabaseURL)
	ok(t, "open owned verification publication gate connection", err)
	gateKey := int64(0x4d31335665726966)
	auditName := "m13_verification_publications_" + nonce(t)
	functionName := "m13_verification_gate_" + nonce(t)
	triggerName := "m13_verification_gate_" + nonce(t)
	auditTable := pgx.Identifier{h.services.cfg.Schema, auditName}.Sanitize()
	function := pgx.Identifier{h.services.cfg.Schema, functionName}.Sanitize()
	trigger := pgx.Identifier{triggerName}.Sanitize()
	jobsTable := verificationTable(h, "verification_jobs")
	_, err = h.services.db.Exec(h.services.ctx, `CREATE TABLE `+auditTable+` (
			worker_id text NOT NULL, fence bigint NOT NULL, job_id text NOT NULL, outcome text NOT NULL);
		CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $m13$
		BEGIN
			IF OLD.state='processing' AND NEW.state='succeeded' THEN
				PERFORM pg_advisory_xact_lock(`+fmt.Sprint(gateKey)+`);
				INSERT INTO `+auditTable+` (worker_id,fence,job_id,outcome)
				VALUES(OLD.worker_id,OLD.fence,NEW.id,NEW.outcome);
			END IF;
			RETURN NEW;
		END
		$m13$;
		CREATE TRIGGER `+trigger+` BEFORE UPDATE ON `+jobsTable+`
		FOR EACH ROW EXECUTE FUNCTION `+function+`()`)
	ok(t, "install owned verification publication witness", err)
	locked := true
	_, err = databaseGate.Exec(h.services.ctx, `SELECT pg_advisory_lock($1)`, gateKey)
	ok(t, "hold owned verification publication gate", err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if locked {
			_, _ = databaseGate.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, gateKey)
		}
		_, _ = h.services.db.Exec(cleanup, `DROP TRIGGER IF EXISTS `+trigger+` ON `+jobsTable+`;
			DROP FUNCTION IF EXISTS `+function+`();
			DROP TABLE IF EXISTS `+auditTable)
		_ = databaseGate.Close(cleanup)
	})

	first := startM13VerificationCommand(t, h)
	processing := waitM13VerificationRow(t, h, queued.ID, func(row verificationJobRow) bool {
		return row.State == "processing" && row.Attempts == 1 && row.Fence == 1 && row.WorkerID != nil
	}, "first real command claim")
	if processing.WorkerID == nil || !m13VerificationWorkerID.MatchString(*processing.WorkerID) ||
		*processing.WorkerID == "aspm-verification" || processing.LeaseUntil == nil {
		t.Fatal("first real command did not use a generated schema-safe durable worker identity and bounded lease")
	}
	firstWorker := *processing.WorkerID
	waitM13AdvisoryPublication(t, h)
	first.requireSurfaces(t)
	first.kill(t)

	afterCrash := readVerificationJobRow(t, h, queued.ID)
	if afterCrash.State != "processing" || afterCrash.Attempts != 1 || afterCrash.Fence != 1 ||
		afterCrash.WorkerID == nil || *afterCrash.WorkerID != firstWorker ||
		afterCrash.Outcome != nil || afterCrash.CompletedAt != nil {
		t.Fatal("crashed command ambiguously published or rewrote the leased V27 job")
	}
	var publications int
	ok(t, "count rolled-back stale verification publications",
		h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+auditTable).Scan(&publications))
	if publications != 0 {
		t.Fatal("stale first worker publication committed despite exact process termination")
	}
	approvalRow := readVerificationApprovalRow(t, h, approval.ID)
	if approvalRow.RevokedAt != nil || !approvalRow.ExpiresAt.After(time.Now().UTC()) ||
		approvalRow.FindingEvidenceRevision != finding.EvidenceRevision {
		t.Fatal("restart fixture lost the current explicit V27 approval before recovery")
	}

	var unlocked bool
	ok(t, "release owned verification publication gate",
		databaseGate.QueryRow(h.services.ctx, `SELECT pg_advisory_unlock($1)`, gateKey).Scan(&unlocked))
	if !unlocked {
		t.Fatal("owned verification publication gate was not held by its dedicated connection")
	}
	locked = false
	waitM13VerificationRow(t, h, queued.ID, func(row verificationJobRow) bool {
		return row.LeaseUntil != nil && time.Now().After(*row.LeaseUntil)
	}, "expired crashed verification lease")

	second := startM13VerificationCommand(t, h)
	settled := waitM13VerificationRow(t, h, queued.ID, func(row verificationJobRow) bool {
		return row.State == "succeeded"
	}, "fresh command terminal settlement")
	second.requireSurfaces(t)
	second.kill(t)
	if settled.Attempts != 2 || settled.Fence != 2 || settled.WorkerID != nil ||
		settled.LeaseUntil != nil || settled.CompletedAt == nil || settled.Outcome == nil ||
		*settled.Outcome != "reproduced" || settled.FailureCode != nil ||
		settled.CloseFinding || settled.FalsePositive {
		t.Fatal("fresh command did not reclaim and atomically settle the V27 job exactly once")
	}
	rows, err := h.services.db.Query(h.services.ctx, `SELECT worker_id,fence,job_id,outcome FROM `+auditTable)
	ok(t, "read committed verification publication witness", err)
	var publicationWorker, publicationJob, publicationOutcome string
	var publicationFence int64
	for rows.Next() {
		publications++
		ok(t, "scan committed verification publication witness",
			rows.Scan(&publicationWorker, &publicationFence, &publicationJob, &publicationOutcome))
	}
	ok(t, "finish committed verification publication witness", rows.Err())
	rows.Close()
	if publications != 1 || !m13VerificationWorkerID.MatchString(publicationWorker) ||
		publicationWorker == firstWorker || publicationFence != 2 ||
		publicationJob != queued.ID || publicationOutcome != "reproduced" {
		t.Fatal("restart produced a duplicate, stale-worker or mismatched terminal publication")
	}
	result := getVerification(t, h, h.admin, finding.ID, queued.ID, http.StatusOK).Verification
	if result.State != "succeeded" || result.Result == nil ||
		result.Result.Outcome != "reproduced" || result.Result.CloseFinding ||
		result.Result.FalsePositive {
		t.Fatal("real command restart exposed an unsafe or incomplete verification result")
	}
	terminal := readVerificationJobRow(t, h, queued.ID)
	if !reflect.DeepEqual(h.finding(h.admin, finding.ID), findingBefore) {
		t.Fatal("verification restart changed finding workflow, disposition, source, AI or closure state")
	}

	third := startM13VerificationCommand(t, h)
	time.Sleep(550 * time.Millisecond)
	third.requireSurfaces(t)
	third.kill(t)
	if !reflect.DeepEqual(readVerificationJobRow(t, h, queued.ID), terminal) ||
		!reflect.DeepEqual(h.finding(h.admin, finding.ID), findingBefore) {
		t.Fatal("empty-queue restart mutated a terminal V27 job or finding")
	}
	var finalPublications int
	ok(t, "count final verification publications",
		h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+auditTable).Scan(&finalPublications))
	if finalPublications != 1 {
		t.Fatal("quiescent restart duplicated terminal verification publication")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("verification command intake, processing, crash recovery or readiness performed object-store I/O")
	}
}
