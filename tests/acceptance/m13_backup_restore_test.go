//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type m13RecoveryConfiguration struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	InstanceID string `json:"instanceId"`
	TargetKind string `json:"targetKind"`
	Database   struct {
		URL    string `json:"url"`
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"database"`
	Storage struct {
		Endpoint         string `json:"endpoint"`
		Region           string `json:"region"`
		Bucket           string `json:"bucket"`
		AccessKey        string `json:"accessKey"`
		SecretKey        string `json:"secretKey"`
		RawPrefix        string `json:"rawPrefix"`
		NormalizedPrefix string `json:"normalizedPrefix"`
		ArchivePrefix    string `json:"archivePrefix"`
	} `json:"storage"`
	Tools struct {
		PGDump          string `json:"pgDump"`
		PGRestore       string `json:"pgRestore"`
		PSQL            string `json:"psql"`
		RequiredVersion string `json:"requiredVersion"`
	} `json:"tools"`
	Limits struct {
		MaxDatabaseBytes    int64 `json:"maxDatabaseBytes"`
		MaxObjects          int   `json:"maxObjects"`
		MaxObjectBytes      int64 `json:"maxObjectBytes"`
		MaxTotalObjectBytes int64 `json:"maxTotalObjectBytes"`
	} `json:"limits"`
}

type m13RecoveryPlan struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Operation  string `json:"operation"`
	InstanceID string `json:"instanceId"`
	ReadOnly   bool   `json:"readOnly"`
}

type m13RecoveryReceipt struct {
	APIVersion     string `json:"apiVersion"`
	Kind           string `json:"kind"`
	Phase          string `json:"phase"`
	BackupID       string `json:"backupId"`
	RestoreID      string `json:"restoreId,omitempty"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type m13RecoveryRestoreState struct {
	APIVersion     string   `json:"apiVersion"`
	Kind           string   `json:"kind"`
	Phase          string   `json:"phase"`
	BackupID       string   `json:"backupId"`
	RestoreID      string   `json:"restoreId"`
	ManifestSHA256 string   `json:"manifestSha256"`
	InstanceID     string   `json:"instanceId"`
	Schema         string   `json:"schema"`
	CreatedKeys    []string `json:"createdKeys"`
	PendingKey     string   `json:"pendingKey,omitempty"`
	SchemaCreated  bool     `json:"schemaCreated"`
	SchemaPending  bool     `json:"schemaPending,omitempty"`
	UpdatedAt      string   `json:"updatedAt"`
	Failure        string   `json:"failure,omitempty"`
}

type m13RecoveryTableWitness struct {
	Rows   int64
	SHA256 string
}

type m13RecoveryDatabaseWitness struct {
	Tables    map[string]m13RecoveryTableWitness
	Ephemeral map[string]int64
}

type m13RecoveryObjectWitness struct {
	Purpose     string
	SizeBytes   int64
	SHA256      string
	ContentType string
	Metadata    map[string]string
}

type m13RecoveryAIProfile struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Revision string `json:"revision"`
}

type m13RecoveryAIPolicy struct {
	Revision string `json:"revision"`
}

type m13RecoveryAIGrant struct {
	ID string `json:"id"`
}

type m13RecoveryAssessmentPreview struct {
	ID string `json:"id"`
}

type m13RecoveryCommandOutput struct {
	stdout, stderr []byte
	err            error
}

type m13RecoveryLimitedBuffer struct {
	data bytes.Buffer
}

func (b *m13RecoveryLimitedBuffer) Write(data []byte) (int, error) {
	if b.data.Len()+len(data) > 1<<20 {
		return 0, errors.New("recovery command output exceeded 1 MiB")
	}
	return b.data.Write(data)
}

func m13RecoveryInstanceID(t *testing.T) string {
	t.Helper()
	raw := nonce(t) + nonce(t)[:8]
	return fmt.Sprintf("%s-%s-4%s-8%s-%s", raw[:8], raw[8:12], raw[13:16], raw[17:20], raw[20:32])
}

func m13RecoveryWorkDirectory(t *testing.T) string {
	t.Helper()
	base := os.Getenv("ASPM_RECOVERY_ACCEPTANCE_ROOT")
	if base == "" || !filepath.IsAbs(base) {
		t.Fatal("BLOCKED: ASPM_RECOVERY_ACCEPTANCE_ROOT must select an absolute repository-local work directory")
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		t.Fatal("BLOCKED: recovery acceptance work root is unavailable")
	}
	path := filepath.Join(base, "case-"+nonce(t))
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("create owned recovery acceptance directory: %v", err)
	}
	t.Cleanup(func() {
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(base)+string(filepath.Separator)) {
			t.Error("refusing cleanup outside the selected recovery acceptance root")
			return
		}
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove owned recovery acceptance directory: %v", err)
		}
	})
	return path
}

func m13RecoveryTool(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("BLOCKED: %s must select an absolute operator-supplied executable", name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("BLOCKED: %s does not select a regular executable", name)
	}
	return path
}

func m13RecoveryDatabaseName(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal("fixture database URL is invalid; value withheld")
	}
	name, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || name == "" || strings.Contains(name, "/") {
		t.Fatal("fixture database URL does not select one explicit database")
	}
	return name
}

func m13RecoveryWriteConfiguration(t *testing.T, path string, config m13RecoveryConfiguration) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode private recovery configuration: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write private recovery configuration: %v", err)
	}
}

func m13RecoveryNewHarness(t *testing.T) *harness {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 10, 9, 19, 0, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.ApplicationName = "aspm-core"
	h.services.cfg.BootstrapToken = secret(t)
	key := sha256.Sum256([]byte(secret(t)))
	h.services.cfg.IntegrationEncryptionKey = append([]byte(nil), key[:]...)
	h.services.cfg.AssessmentScope = "m13-recovery-advisories"
	h.services.cfg.PublicOrigin = "https://aspm.test"
	h.services.cfg.LogOutput = io.Discard
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close M13 recovery application", h.app.Close())
			h.app = Application{}
		}
	})
	h.enroll()
	return h
}

func m13RecoverySeedArchive(t *testing.T, h *harness) {
	t.Helper()
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	approved, queued, _ := v24QueueHistory(t, h)
	if queued.Total != len(fixture.payloads) || approved.State != "approved" {
		t.Fatal("M13 recovery archive fixture did not create one exact approved V24 run")
	}
	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close M13 recovery archive worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, http.StatusOK)
	if completed.State != "succeeded" || completed.Succeeded < 1 {
		t.Fatal("M13 recovery fixture did not publish referenced archive bytes")
	}
	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
		notificationTable(h, "finding_change_events")+`
		SET state='pending',worker_id=NULL,fence=0,lease_until=NULL
		WHERE workspace_id=$1 AND state='processing'`, h.admin.workspace)
	ok(t, "return synthetic held policy event to a quiescent pending state", err)
}

func m13RecoverySeedCorrelation(t *testing.T, h *harness, analyst actor) finding {
	t.Helper()
	work := h.work(h.admin, "")
	if len(work) < 2 {
		t.Fatal("M13 recovery fixture needs the two retained history findings")
	}
	primary := h.finding(h.admin, work[0].ID)
	if len(primary.Observations) == 0 {
		t.Fatal("M13 recovery primary finding needs one immutable observation")
	}
	primaryObservationID := primary.Observations[0].ID
	report := sarif(t, func(_ object, result object) {
		result["guid"] = "13131313-1313-4313-8313-131313131313"
		result["message"] = object{"text": "Independent M13 recovery correlation source."}
	})
	input := h.input(primary.AssetID, "sarif", report)
	input["sourceId"], input["scanId"] = "m13-recovery-source-b", "m13-recovery-scan-b"
	input["sourceScanAt"], input["collectedAt"] = sourceTime.Add(48*time.Hour), sourceTime.Add(49*time.Hour)
	run := h.finish(h.upload(input).ID, "succeeded")
	secondary := retentionFindingForRun(t, h, run.RunID)
	expiry := h.services.cfg.Now().Add(72 * time.Hour)
	h.json(h.admin, http.MethodPatch, "/api/v1/findings/"+primary.ID, object{
		"ownerId": h.admin.user.ID, "workflowState": "open",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": expiry,
		"rationale": "Retain one explicit M13 recovery disposition approval.",
	}, http.StatusOK)
	h.json(h.admin, http.MethodPatch, "/api/v1/findings/"+secondary.ID, object{
		"ownerId": analyst.user.ID, "workflowState": "in-progress", "disposition": "none",
		"rationale": "Retain the independent M13 correlation member authority.",
	}, http.StatusOK)
	preview := h.json(h.admin, http.MethodPost, "/api/v1/findings/"+primary.ID+"/merge-previews",
		object{"otherFindingId": secondary.ID}, http.StatusOK).MergePreview
	created := h.json(h.admin, http.MethodPost, "/api/v1/findings/"+primary.ID+"/merges", object{
		"otherFindingId":          secondary.ID,
		"primaryDecisionRevision": preview.Primary.DecisionRevision,
		"primaryEvidenceRevision": preview.Primary.EvidenceRevision,
		"otherDecisionRevision":   preview.Other.DecisionRevision,
		"otherEvidenceRevision":   preview.Other.EvidenceRevision,
		"decision":                correlationDecision(&analyst.user.ID, "in-progress", "accepted-risk", &expiry),
		"rationale":               "Preserve two independently observed source variants through M13 recovery.",
		"idempotencyKey":          "m13-recovery-correlation",
	}, http.StatusCreated).Correlation
	if created.State != "active" || len(created.Members) != 2 || len(created.Events) != 1 {
		t.Fatal("M13 recovery fixture did not retain one reviewed active correlation")
	}
	result := h.finding(h.admin, primary.ID)
	for index := range result.Observations {
		if result.Observations[index].ID == primaryObservationID {
			result.Observations[0], result.Observations[index] =
				result.Observations[index], result.Observations[0]
			return result
		}
	}
	t.Fatal("M13 recovery primary observation disappeared after correlation")
	return finding{}
}

func m13RecoveryDecode[T any](t *testing.T, response *httptestResponse, target *T, field string) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(response.body, &envelope); err != nil {
		t.Fatalf("decode M13 recovery seed envelope: %v", err)
	}
	raw, present := envelope[field]
	if !present {
		t.Fatalf("M13 recovery seed envelope omitted %s", field)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode M13 recovery seed %s: %v", field, err)
	}
}

func m13RecoveryAPI(t *testing.T, h *harness, who actor, method, path string, body any, want int) *httptestResponse {
	t.Helper()
	var data []byte
	if body != nil {
		data = encode(t, body)
	}
	response := h.request(who, method, path, data, want)
	return &httptestResponse{body: bytes.Clone(response.Body.Bytes())}
}

func m13RecoverySeedAI(t *testing.T, h *harness, analyst actor, target finding, apiKey string) {
	t.Helper()
	var profile m13RecoveryAIProfile
	m13RecoveryDecode(t, m13RecoveryAPI(t, h, h.admin, http.MethodPost, "/api/v1/ai/profiles", object{
		"name": "M13 recovery hosted profile", "family": "openai",
		"endpoint": "https://api.openai.com/v1", "model": "m13-recovery-model",
		"deployment": "", "enabled": true, "structuredOutput": true, "apiKey": apiKey,
	}, http.StatusCreated), &profile, "profile")
	if profile.ID == "" || profile.Revision != "1" {
		t.Fatal("M13 recovery AI profile did not retain a versioned identity")
	}
	var policy m13RecoveryAIPolicy
	m13RecoveryDecode(t, m13RecoveryAPI(t, h, h.admin, http.MethodPatch, "/api/v1/ai/policy",
		object{"mode": "approved-hosted"}, http.StatusOK), &policy, "policy")
	var grant m13RecoveryAIGrant
	m13RecoveryDecode(t, m13RecoveryAPI(t, h, h.admin, http.MethodPost, "/api/v1/ai/grants", object{
		"profileId": profile.ID, "profileRevision": profile.Revision,
		"policyRevision": policy.Revision, "destination": profile.Endpoint,
		"task": "finding-validity", "dataClass": "finding-evidence",
		"expiresAt": h.services.cfg.Now().Add(24 * time.Hour).Format(time.RFC3339Nano),
	}, http.StatusCreated), &grant, "grant")
	if len(target.Observations) == 0 {
		t.Fatal("M13 recovery AI advisory fixture needs an immutable observation")
	}
	var preview m13RecoveryAssessmentPreview
	m13RecoveryDecode(t, m13RecoveryAPI(t, h, analyst, http.MethodPost,
		"/api/v1/findings/"+target.ID+"/assessment-previews", object{
			"observationId": target.Observations[0].ID, "profileId": profile.ID,
			"grantId": grant.ID, "context": "Reviewed synthetic M13 recovery advisory context.",
			"reviewed": true,
		}, http.StatusCreated), &preview, "preview")
	var assessment struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	m13RecoveryDecode(t, m13RecoveryAPI(t, h, analyst, http.MethodPost,
		"/api/v1/findings/"+target.ID+"/assessments", object{
			"previewId": preview.ID, "idempotencyKey": "m13-recovery-advisory", "consent": true,
		}, http.StatusAccepted), &assessment, "assessment")
	if assessment.ID == "" || assessment.State != "queued" {
		t.Fatal("M13 recovery fixture did not retain one queued reviewed AI advisory")
	}
}

func m13RecoverySeedReport(t *testing.T, h *harness) {
	t.Helper()
	completed := h.services.cfg.Now().Add(-time.Minute)
	report := historicalReport(h.admin.workspace, completed, 2)
	snapshot := historicalSnapshotSeed{
		ID: strings.Repeat("d", 32), WorkspaceID: h.admin.workspace,
		RequestedBy: h.admin.user.ID, Name: "M13 recovery immutable report",
		State: "succeeded", CompletedAt: &completed, Report: &report,
	}
	seedHistoricalSnapshot(t, h, snapshot)
	export, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
		"m13-recovery-report-export", http.StatusAccepted)
	ctx, cancel := context.WithTimeout(h.services.ctx, 10*time.Second)
	defer cancel()
	ok(t, "process M13 recovery report export", h.app.ProcessReports(ctx))
	settled := getReportExport(t, h, h.admin, export.Export.ID, http.StatusOK).Export
	if settled.State != "succeeded" || settled.Digest == nil || settled.SizeBytes == nil {
		t.Fatal("M13 recovery fixture did not retain an exact report export")
	}
}

func m13RecoverySeedEphemeralRows(t *testing.T, h *harness) {
	t.Helper()
	flows := pgx.Identifier{h.services.cfg.Schema, "app_oidc_flows"}.Sanitize()
	throttle := pgx.Identifier{h.services.cfg.Schema, "app_auth_throttle"}.Sanitize()
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+flows+`
		(state_hash,browser_hash,nonce_hash,issuer,client_id,workspace_id,redirect_uri,created_at,expires_at)
		VALUES(decode(repeat('11',32),'hex'),decode(repeat('22',32),'hex'),decode(repeat('33',32),'hex'),
		'https://id.example.invalid','m13-client',$1,'https://aspm.test/oidc/callback',$2,$3)`,
		h.admin.workspace, h.services.cfg.Now(), h.services.cfg.Now().Add(5*time.Minute))
	ok(t, "seed one ephemeral OIDC flow", err)
	_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+throttle+`
		(bucket,window_at,attempts) VALUES(1313,$1,1)
		ON CONFLICT(bucket) DO UPDATE SET window_at=EXCLUDED.window_at,attempts=EXCLUDED.attempts`,
		h.services.cfg.Now())
	ok(t, "seed one ephemeral auth throttle row", err)
}

func m13RecoveryTransitionalRows(t *testing.T, pool *pgxpool.Pool, schema string) int64 {
	t.Helper()
	specs := []struct {
		table, predicate string
	}{
		{"imports", `state='processing'`},
		{"report_snapshots", `state='processing'`},
		{"report_exports", `state='processing'`},
		{"finding_deliveries", `state='dispatching'`},
		{"source_collections", `state='collecting'`},
		{"assessment_jobs", `state='dispatching' OR (io_token IS NOT NULL AND io_released_at IS NULL)`},
		{"verification_jobs", `state='processing'`},
		{"finding_change_events", `state='processing'`},
		{"retention_runs", `state='processing'`},
		{"retention_run_items", `state='processing'`},
		{"archive_publications", `state='publishing'`},
	}
	var total int64
	for _, spec := range specs {
		var count int64
		query := `SELECT count(*) FROM ` +
			pgx.Identifier{schema, "app_" + spec.table}.Sanitize() + ` WHERE ` + spec.predicate
		ok(t, "count M13 recovery transitional "+spec.table, pool.QueryRow(context.Background(), query).Scan(&count))
		total += count
	}
	return total
}

func m13RecoveryDatabaseSnapshot(t *testing.T, pool *pgxpool.Pool, schema string) m13RecoveryDatabaseWitness {
	t.Helper()
	result := m13RecoveryDatabaseWitness{
		Tables:    map[string]m13RecoveryTableWitness{},
		Ephemeral: map[string]int64{},
	}
	ephemeral := map[string]bool{
		"app_auth_throttle": true,
		"app_oidc_flows":    true,
		"app_sessions":      true,
	}
	for _, name := range m13RecoveryV27Tables {
		table := pgx.Identifier{schema, name}.Sanitize()
		if ephemeral[name] {
			var count int64
			ok(t, "count source ephemeral "+name,
				pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count))
			result.Ephemeral[name] = count
			continue
		}
		var count int64
		var canonical string
		ok(t, "read exact table witness "+name, pool.QueryRow(context.Background(), `SELECT count(*),
			COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text),
			'[]'::jsonb)::text FROM `+table+` AS row_value`).Scan(&count, &canonical))
		result.Tables[name] = m13RecoveryTableWitness{Rows: count, SHA256: digest([]byte(canonical))}
	}
	return result
}

func m13RecoveryRequireRepresentativeState(t *testing.T, witness m13RecoveryDatabaseWitness) {
	t.Helper()
	for _, name := range []string{
		"app_assets", "app_imports", "app_observations", "app_findings",
		"app_finding_decision_events", "app_finding_disposition_approvals",
		"app_finding_correlations", "app_finding_correlation_members",
		"app_finding_correlation_events", "app_retention_previews",
		"app_retention_runs", "app_retention_run_items", "app_archive_publications",
		"app_integration_connections", "app_finding_deliveries",
		"app_notification_policies", "app_notification_policy_revisions",
		"app_notification_policy_events", "app_ai_profiles", "app_ai_policies",
		"app_ai_egress_grants", "app_assessment_previews", "app_assessment_jobs",
		"app_report_snapshots", "app_report_exports", "app_verification_evidence",
		"app_verification_approvals", "app_verification_jobs", "app_users",
		"app_workspaces", "app_memberships", "app_schema_versions",
	} {
		if witness.Tables[name].Rows < 1 {
			t.Fatalf("M13 recovery source omitted representative durable state in %s", name)
		}
	}
	if witness.Tables["app_schema_versions"].Rows != 27 ||
		witness.Ephemeral["app_sessions"] < 1 ||
		witness.Ephemeral["app_oidc_flows"] < 1 ||
		witness.Ephemeral["app_auth_throttle"] < 1 {
		t.Fatal("M13 recovery source omitted the exact V27 ledger or explicit ephemeral exclusions")
	}
}

func m13RecoveryObjects(t *testing.T, h *harness, prefixes map[string]string) map[string]m13RecoveryObjectWitness {
	t.Helper()
	result := map[string]m13RecoveryObjectWitness{}
	for purpose, prefix := range prefixes {
		pages := s3.NewListObjectsV2Paginator(h.services.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(h.services.cfg.Storage.Bucket),
			Prefix: aws.String(prefix), MaxKeys: aws.Int32(100),
		})
		for page := 0; pages.HasMorePages(); page++ {
			if page >= 20 {
				t.Fatal("M13 recovery object witness exceeded its bounded page count")
			}
			list, err := pages.NextPage(h.services.ctx)
			ok(t, "list M13 recovery "+purpose+" objects", err)
			for _, item := range list.Contents {
				key := aws.ToString(item.Key)
				if !strings.HasPrefix(key, prefix) {
					t.Fatal("M13 recovery object listing escaped its selected prefix")
				}
				object, err := h.services.s3.GetObject(h.services.ctx, &s3.GetObjectInput{
					Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
				})
				ok(t, "read M13 recovery object bytes", err)
				data, readErr := io.ReadAll(io.LimitReader(object.Body, (64<<20)+1))
				closeErr := object.Body.Close()
				ok(t, "read bounded M13 recovery object", readErr)
				ok(t, "close M13 recovery object", closeErr)
				if len(data) > 64<<20 {
					t.Fatal("M13 recovery fixture object exceeded 64 MiB")
				}
				metadata := map[string]string{}
				for name, value := range object.Metadata {
					metadata[strings.ToLower(name)] = value
				}
				result[key] = m13RecoveryObjectWitness{
					Purpose: purpose, SizeBytes: int64(len(data)), SHA256: digest(data),
					ContentType: aws.ToString(object.ContentType), Metadata: metadata,
				}
			}
		}
	}
	return result
}

func m13RecoveryDeleteObjects(t *testing.T, h *harness, prefixes map[string]string) {
	t.Helper()
	for _, prefix := range prefixes {
		pages := s3.NewListObjectsV2Paginator(h.services.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(h.services.cfg.Storage.Bucket),
			Prefix: aws.String(prefix), MaxKeys: aws.Int32(100),
		})
		for page := 0; pages.HasMorePages(); page++ {
			if page >= 20 {
				t.Fatal("M13 recovery disaster cleanup exceeded its bounded page count")
			}
			list, err := pages.NextPage(h.services.ctx)
			ok(t, "list exact M13 disaster-simulation objects", err)
			for _, item := range list.Contents {
				key := aws.ToString(item.Key)
				if !strings.HasPrefix(key, prefix) {
					t.Fatal("refusing M13 disaster-simulation delete outside selected prefix")
				}
				_, err := h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
					Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
				})
				ok(t, "delete exact M13 disaster-simulation object", err)
			}
		}
	}
}

func m13RecoveryRun(t *testing.T, private []string, success bool, args ...string) m13RecoveryCommandOutput {
	t.Helper()
	binary := m13RecoveryTool(t, "ASPM_RECOVERY_BINARY")
	command := exec.Command(binary, args...)
	command.Dir = filepath.Join("..", "..")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			strings.HasPrefix(upper, "PG") || upper == "KUBECONFIG" ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "AWS_EC2_METADATA_DISABLED=true")
	command.WaitDelay = 2 * time.Second
	var stdout, stderr m13RecoveryLimitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	output := m13RecoveryCommandOutput{
		stdout: bytes.Clone(stdout.data.Bytes()),
		stderr: bytes.Clone(stderr.data.Bytes()),
		err:    err,
	}
	for _, value := range private {
		if value == "" {
			continue
		}
		for _, data := range [][]byte{output.stdout, output.stderr} {
			if bytes.Contains(data, []byte(value)) {
				t.Fatal("recovery command output exposed private configuration")
			}
		}
	}
	if success && err != nil {
		t.Fatalf("recovery command %v failed (%T; output withheld)", args, err)
	}
	if !success && err == nil {
		t.Fatalf("unsafe recovery command %v unexpectedly succeeded", args)
	}
	return output
}

func m13RecoveryPlanOutput(t *testing.T, output m13RecoveryCommandOutput, kind, operation, instance string) m13RecoveryPlan {
	t.Helper()
	var plan m13RecoveryPlan
	m13RecoveryStrictJSON(t, output.stdout, &plan, "M13 recovery plan output")
	if plan.APIVersion != m13RecoveryAPIVersion || plan.Kind != kind ||
		plan.Operation != operation || plan.InstanceID != instance ||
		plan.ID == "" || !plan.ReadOnly {
		t.Fatal("recovery plan did not expose one explicit read-only approval identity")
	}
	return plan
}

func m13RecoveryReceiptOutput(t *testing.T, output m13RecoveryCommandOutput, kind, phase string) m13RecoveryReceipt {
	t.Helper()
	var receipt m13RecoveryReceipt
	m13RecoveryStrictJSON(t, output.stdout, &receipt, "M13 recovery receipt output")
	if receipt.APIVersion != m13RecoveryAPIVersion || receipt.Kind != kind ||
		receipt.Phase != phase || receipt.BackupID == "" ||
		!m13RecoveryDigest.MatchString(receipt.ManifestSHA256) {
		t.Fatal("recovery receipt omitted exact backup identity, checksum or truthful phase")
	}
	return receipt
}

func m13RecoveryScanBackup(t *testing.T, root string, private [][]byte) {
	t.Helper()
	allowedRoot := map[string]bool{
		"manifest.json": true, "manifest.sha256": true, "database.dump": true,
		"database-inventory.json": true, "objects.jsonl": true, "objects": true,
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read completed backup directory: %v", err)
	}
	for _, entry := range entries {
		if !allowedRoot[entry.Name()] {
			t.Fatalf("completed backup contains undeclared root artifact %q", entry.Name())
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range private {
			if len(value) > 0 && bytes.Contains(data, value) {
				return errors.New("completed backup contains plaintext private runtime material")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan completed backup secrecy boundary: %v", err)
	}
}

func m13RecoveryAssertManifest(t *testing.T, backup string, config m13RecoveryConfiguration) m13RecoveryManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		t.Fatalf("read completed recovery manifest: %v", err)
	}
	var manifest m13RecoveryManifest
	m13RecoveryStrictJSON(t, raw, &manifest, "completed M13 recovery manifest")
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("re-encode completed recovery manifest: %v", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) {
		t.Fatal("completed recovery manifest is not canonical LF-terminated JSON")
	}
	checksum, err := os.ReadFile(filepath.Join(backup, "manifest.sha256"))
	if err != nil {
		t.Fatalf("read completed manifest checksum: %v", err)
	}
	want := m13RecoveryHash(raw) + "\n"
	if string(checksum) != want {
		t.Fatal("manifest checksum does not bind the exact canonical bytes")
	}
	if manifest.Source.InstanceID != config.InstanceID ||
		manifest.Source.TargetKind != config.TargetKind ||
		manifest.Source.Database.Name != config.Database.Name ||
		manifest.Source.Database.Schema != config.Database.Schema ||
		manifest.Source.Storage.Bucket != config.Storage.Bucket ||
		manifest.Source.Storage.Region != config.Storage.Region ||
		!reflect.DeepEqual(manifest.Source.Storage.Prefixes, []m13RecoveryPrefix{
			{Purpose: "raw", Prefix: config.Storage.RawPrefix},
			{Purpose: "normalized", Prefix: config.Storage.NormalizedPrefix},
			{Purpose: "archive", Prefix: config.Storage.ArchivePrefix},
		}) {
		t.Fatal("completed manifest changed the explicitly selected logical source scope")
	}
	if manifest.Source.Database.SchemaVersion != 27 ||
		manifest.Consistency.Mode != "quiesced-single-instance" ||
		manifest.Consistency.DistributedAtomic ||
		manifest.Consistency.ApplicationConnections != 0 ||
		manifest.Consistency.TransitionalRows != 0 {
		t.Fatal("completed manifest made an unsupported schema or consistency claim")
	}
	for _, path := range []string{
		manifest.Database.Dump.Path,
		manifest.Database.Inventory.Path,
		manifest.Objects.Index.Path,
	} {
		m13RecoveryLocalPath(t, path)
	}
	lower := strings.ToLower(string(raw))
	for _, value := range []string{
		config.Database.URL, config.Storage.Endpoint, config.Storage.AccessKey,
		config.Storage.SecretKey, config.Tools.PGDump, config.Tools.PGRestore, config.Tools.PSQL,
	} {
		if value != "" && strings.Contains(lower, strings.ToLower(value)) {
			t.Fatal("portable manifest exposed a credential, endpoint, URL or host tool path")
		}
	}
	return manifest
}

func m13RecoverySchemaExists(t *testing.T, pool *pgxpool.Pool, schema string) bool {
	t.Helper()
	var exists bool
	ok(t, "inspect exact M13 recovery schema existence", pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists))
	return exists
}

func m13RecoveryVerifyReferences(t *testing.T, pool *pgxpool.Pool, schema string,
	objects map[string]m13RecoveryObjectWitness) {
	t.Helper()
	type reference struct {
		key, digest string
		size        int64
	}
	var refs []reference
	imports := pgx.Identifier{schema, "app_imports"}.Sanitize()
	rows, err := pool.Query(context.Background(), `SELECT report_key,report_digest,report_size
		FROM `+imports+` WHERE evidence_availability='available'`)
	ok(t, "read restored import object references", err)
	for rows.Next() {
		var item reference
		ok(t, "scan restored import object reference", rows.Scan(&item.key, &item.digest, &item.size))
		refs = append(refs, item)
	}
	ok(t, "finish restored import object references", rows.Err())
	rows.Close()
	for _, table := range []string{
		"observations", "finding_correlation_events", "finding_decision_events",
		"notification_policy_revisions", "finding_change_events", "notification_policy_events",
	} {
		rows, err := pool.Query(context.Background(), `SELECT archive_key,archive_digest,archive_size FROM `+
			pgx.Identifier{schema, "app_" + table}.Sanitize()+` WHERE archive_key IS NOT NULL`)
		ok(t, "read restored "+table+" archive references", err)
		for rows.Next() {
			var item reference
			ok(t, "scan restored "+table+" archive reference", rows.Scan(&item.key, &item.digest, &item.size))
			refs = append(refs, item)
		}
		ok(t, "finish restored "+table+" archive references", rows.Err())
		rows.Close()
	}
	if len(refs) == 0 {
		t.Fatal("M13 recovery fixture did not retain any PostgreSQL object references")
	}
	for _, ref := range refs {
		object, present := objects[ref.key]
		if !present || object.SHA256 != ref.digest || object.SizeBytes != ref.size {
			t.Fatalf("restored PostgreSQL reference %q does not match exact restored object bytes", ref.key)
		}
	}
}

func TestM13RecoveryQuiescedV27BackupRestoreAndPostRestoreWorkflow(t *testing.T) {
	h := m13RecoveryNewHarness(t)
	work := m13RecoveryWorkDirectory(t)
	fixtureID := strings.TrimPrefix(h.services.cfg.Schema, "acceptance_")
	normalizedPrefix := "acceptance-normalized/" + fixtureID + "/"
	prefixes := map[string]string{
		"raw": h.services.cfg.Storage.Prefix, "normalized": normalizedPrefix,
		"archive": h.services.cfg.ArchiveStorage.Prefix,
	}
	t.Cleanup(func() {
		if h.app.Close != nil {
			_ = h.app.Close()
			h.app = Application{}
		}
		if !m13RecoverySchemaExists(t, h.services.db, h.services.cfg.Schema) {
			_, err := h.services.db.Exec(context.Background(),
				"CREATE SCHEMA "+pgx.Identifier{h.services.cfg.Schema}.Sanitize())
			if err != nil {
				t.Errorf("recreate empty owned schema for fixture cleanup: %v", err)
			}
		}
		m13RecoveryDeleteObjects(t, h, map[string]string{"normalized": normalizedPrefix})
	})

	analyst := h.addUser(h.admin, "analyst")
	_ = h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	_ = h.addUser(foreign, "viewer")
	m13RecoverySeedArchive(t, h)
	primary := m13RecoverySeedCorrelation(t, h, analyst)
	aiKey := secret(t) + secret(t)
	m13RecoverySeedAI(t, h, analyst, primary, aiKey)
	m13RecoverySeedReport(t, h)
	_, approval, queuedVerification := seedVerificationBinding(t, h, analyst, primary, true, "recovery")
	if approval.ID == "" || queuedVerification.State != "queued" {
		t.Fatal("M13 recovery fixture did not retain current verification authority")
	}
	m13RecoverySeedEphemeralRows(t, h)
	if transitional := m13RecoveryTransitionalRows(t, h.services.db, h.services.cfg.Schema); transitional != 0 {
		t.Fatalf("M13 source is not quiescent before backup: %d transitional rows", transitional)
	}

	config := m13RecoveryConfiguration{
		APIVersion: m13RecoveryAPIVersion, Kind: "RecoveryConfiguration",
		InstanceID: m13RecoveryInstanceID(t), TargetKind: "linux",
	}
	config.Database.URL = h.services.cfg.DatabaseURL
	config.Database.Name = m13RecoveryDatabaseName(t, h.services.cfg.DatabaseURL)
	config.Database.Schema = h.services.cfg.Schema
	config.Storage.Endpoint, config.Storage.Region = h.services.cfg.Storage.Endpoint, h.services.cfg.Storage.Region
	config.Storage.Bucket = h.services.cfg.Storage.Bucket
	config.Storage.AccessKey, config.Storage.SecretKey = h.services.cfg.Storage.AccessKey, h.services.cfg.Storage.SecretKey
	config.Storage.RawPrefix, config.Storage.NormalizedPrefix = h.services.cfg.Storage.Prefix, normalizedPrefix
	config.Storage.ArchivePrefix = h.services.cfg.ArchiveStorage.Prefix
	config.Tools.PGDump = m13RecoveryTool(t, "ASPM_RECOVERY_PG_DUMP")
	config.Tools.PGRestore = m13RecoveryTool(t, "ASPM_RECOVERY_PG_RESTORE")
	config.Tools.PSQL = m13RecoveryTool(t, "ASPM_RECOVERY_PSQL")
	config.Tools.RequiredVersion = "18.6"
	config.Limits.MaxDatabaseBytes = 256 << 20
	config.Limits.MaxObjects = 1000
	config.Limits.MaxObjectBytes = 64 << 20
	config.Limits.MaxTotalObjectBytes = 256 << 20
	configPath := filepath.Join(work, "recovery.json")
	m13RecoveryWriteConfiguration(t, configPath, config)
	backupPath := filepath.Join(work, "backup")
	restoreState := filepath.Join(work, "restore-state.json")
	privateStrings := []string{
		config.Database.URL, config.Storage.Endpoint, config.Storage.AccessKey, config.Storage.SecretKey,
		h.services.cfg.BootstrapToken, h.password, aiKey,
		hex.EncodeToString(h.services.cfg.IntegrationEncryptionKey),
		base64.StdEncoding.EncodeToString(h.services.cfg.IntegrationEncryptionKey),
	}
	if h.admin.cookie != nil {
		privateStrings = append(privateStrings, h.admin.cookie.Value)
	}

	m13RecoveryRun(t, privateStrings, false,
		"backup", "plan", "--config", configPath, "--backup", backupPath)
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nonquiescent backup plan created a staging or completed directory")
	}

	oldAdmin := h.admin
	ok(t, "close source application for quiesced backup", h.app.Close())
	h.app = Application{}
	var active int
	ok(t, "verify source ASPM application connection closed", h.services.db.QueryRow(h.services.ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE application_name IN
		('aspm-core','aspm-core-application','aspm-ingestion','aspm-retention',
		 'aspm-reports','aspm-delivery','aspm-collection','aspm-assessment','aspm-verification',
		 'aspm-schema-migration')`).Scan(&active))
	if active != 0 || m13RecoveryTransitionalRows(t, h.services.db, h.services.cfg.Schema) != 0 {
		t.Fatal("source did not reach the required single-instance quiescent boundary")
	}
	beforeDatabase := m13RecoveryDatabaseSnapshot(t, h.services.db, h.services.cfg.Schema)
	m13RecoveryRequireRepresentativeState(t, beforeDatabase)
	beforeObjects := m13RecoveryObjects(t, h, prefixes)
	if len(beforeObjects) < 2 {
		t.Fatal("M13 recovery source omitted raw and archive immutable object bytes")
	}

	backupPlan := m13RecoveryPlanOutput(t, m13RecoveryRun(t, privateStrings, true,
		"backup", "plan", "--config", configPath, "--backup", backupPath),
		"BackupPlan", "backup", config.InstanceID)
	backupReceipt := m13RecoveryReceiptOutput(t, m13RecoveryRun(t, privateStrings, true,
		"backup", "create", "--config", configPath, "--backup", backupPath,
		"--approve-plan", backupPlan.ID), "BackupReceipt", "completed")
	verifiedBackup := m13RecoveryReceiptOutput(t, m13RecoveryRun(t, privateStrings, true,
		"backup", "verify", "--config", configPath, "--backup", backupPath),
		"BackupVerification", "verified")
	if verifiedBackup.BackupID != backupReceipt.BackupID ||
		verifiedBackup.ManifestSHA256 != backupReceipt.ManifestSHA256 {
		t.Fatal("backup verification changed the completed backup identity")
	}
	manifest := m13RecoveryAssertManifest(t, backupPath, config)
	if manifest.Objects.ObjectCount != len(beforeObjects) {
		t.Fatal("completed manifest object count does not cover exact selected source prefixes")
	}
	privateBytes := make([][]byte, 0, len(privateStrings)+1)
	for _, value := range privateStrings {
		privateBytes = append(privateBytes, []byte(value))
	}
	privateBytes = append(privateBytes, h.services.cfg.IntegrationEncryptionKey)
	m13RecoveryScanBackup(t, backupPath, privateBytes)

	_, err := h.services.db.Exec(h.services.ctx,
		"DROP SCHEMA "+pgx.Identifier{h.services.cfg.Schema}.Sanitize()+" CASCADE")
	ok(t, "simulate exact owned PostgreSQL schema loss", err)
	m13RecoveryDeleteObjects(t, h, prefixes)
	if m13RecoverySchemaExists(t, h.services.db, h.services.cfg.Schema) ||
		len(m13RecoveryObjects(t, h, prefixes)) != 0 {
		t.Fatal("M13 recovery destination is not explicitly absent and empty")
	}

	wrong := config
	wrong.InstanceID = m13RecoveryInstanceID(t)
	wrongPath := filepath.Join(work, "wrong-instance.json")
	m13RecoveryWriteConfiguration(t, wrongPath, wrong)
	m13RecoveryRun(t, privateStrings, false,
		"restore", "plan", "--config", wrongPath, "--backup", backupPath, "--state", restoreState)
	if m13RecoverySchemaExists(t, h.services.db, h.services.cfg.Schema) ||
		len(m13RecoveryObjects(t, h, prefixes)) != 0 {
		t.Fatal("wrong instance identity changed the empty destination")
	}

	stalePlan := m13RecoveryPlanOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "plan", "--config", configPath, "--backup", backupPath, "--state", restoreState),
		"RestorePlan", "restore", config.InstanceID)
	blockerKey := config.Storage.RawPrefix + "m13-restore-plan-blocker"
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(config.Storage.Bucket), Key: aws.String(blockerKey),
		Body: bytes.NewReader([]byte("stale plan blocker")), ContentLength: aws.Int64(18),
	})
	ok(t, "create exact stale-plan destination blocker", err)
	m13RecoveryRun(t, privateStrings, false,
		"restore", "apply", "--config", configPath, "--backup", backupPath,
		"--state", restoreState, "--approve-plan", stalePlan.ID,
		"--confirm-instance", config.InstanceID)
	if m13RecoverySchemaExists(t, h.services.db, h.services.cfg.Schema) {
		t.Fatal("stale restore approval created a partial database schema")
	}
	_, err = h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(config.Storage.Bucket), Key: aws.String(blockerKey),
	})
	ok(t, "remove exact stale-plan blocker", err)
	if err := os.Remove(restoreState); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove rejected restore state: %v", err)
	}

	var interruptedKey string
	var interruptedObject m13RecoveryObjectWitness
	for key, object := range beforeObjects {
		interruptedKey, interruptedObject = key, object
		break
	}
	interruptedBytes, err := os.ReadFile(filepath.Join(backupPath, "objects", "sha256",
		strings.TrimPrefix(interruptedObject.SHA256, "sha256:")))
	ok(t, "read interrupted restore object fixture", err)
	if digest(interruptedBytes) != interruptedObject.SHA256 {
		t.Fatal("interrupted restore object fixture changed digest")
	}
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(config.Storage.Bucket), Key: aws.String(interruptedKey),
		Body: bytes.NewReader(interruptedBytes), ContentLength: aws.Int64(int64(len(interruptedBytes))),
		ContentType: aws.String(interruptedObject.ContentType), Metadata: interruptedObject.Metadata,
		IfNoneMatch: aws.String("*"),
	})
	ok(t, "create interrupted restore object checkpoint", err)
	_, err = h.services.db.Exec(h.services.ctx,
		"CREATE SCHEMA "+pgx.Identifier{h.services.cfg.Schema}.Sanitize())
	ok(t, "create interrupted restore schema checkpoint", err)
	interruptedState := m13RecoveryRestoreState{
		APIVersion: m13RecoveryAPIVersion, Kind: "RestoreState", Phase: "failed",
		BackupID: backupReceipt.BackupID, RestoreID: strings.Repeat("e", 32),
		ManifestSHA256: backupReceipt.ManifestSHA256,
		InstanceID:     config.InstanceID, Schema: config.Database.Schema,
		CreatedKeys: []string{}, PendingKey: interruptedKey, SchemaPending: true,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Failure:   "synthetic interrupted restore",
	}
	interruptedStateBytes, err := json.Marshal(interruptedState)
	ok(t, "encode interrupted restore journal", err)
	interruptedStateBytes = append(interruptedStateBytes, '\n')
	ok(t, "write interrupted restore journal", os.WriteFile(restoreState, interruptedStateBytes, 0600))
	cleanupPlan := m13RecoveryPlanOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "cleanup", "--config", configPath, "--backup", backupPath,
		"--state", restoreState, "--dry-run"), "RestorePlan", "restore", config.InstanceID)
	appliedCleanup := m13RecoveryPlanOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "cleanup", "--config", configPath, "--backup", backupPath,
		"--state", restoreState, "--approve-plan", cleanupPlan.ID,
		"--confirm-instance", config.InstanceID), "RestorePlan", "restore", config.InstanceID)
	if appliedCleanup.ID != cleanupPlan.ID {
		t.Fatal("restore cleanup changed its approved plan identity")
	}
	if m13RecoverySchemaExists(t, h.services.db, h.services.cfg.Schema) ||
		len(m13RecoveryObjects(t, h, prefixes)) != 0 {
		t.Fatal("approved restore cleanup retained journal-owned schema or objects")
	}
	if _, err = os.Stat(restoreState); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("approved restore cleanup retained its completed journal")
	}

	restorePlan := m13RecoveryPlanOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "plan", "--config", configPath, "--backup", backupPath, "--state", restoreState),
		"RestorePlan", "restore", config.InstanceID)
	restoreReceipt := m13RecoveryReceiptOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "apply", "--config", configPath, "--backup", backupPath,
		"--state", restoreState, "--approve-plan", restorePlan.ID,
		"--confirm-instance", config.InstanceID), "RestoreReceipt", "verified")
	verifiedRestore := m13RecoveryReceiptOutput(t, m13RecoveryRun(t, privateStrings, true,
		"restore", "verify", "--config", configPath, "--backup", backupPath,
		"--state", restoreState), "RestoreVerification", "verified")
	if restoreReceipt.RestoreID == "" || verifiedRestore.RestoreID != restoreReceipt.RestoreID ||
		verifiedRestore.ManifestSHA256 != backupReceipt.ManifestSHA256 {
		t.Fatal("restore verification changed the exact restore or backup identity")
	}

	afterDatabase := m13RecoveryDatabaseSnapshot(t, h.services.db, h.services.cfg.Schema)
	if !reflect.DeepEqual(afterDatabase.Tables, beforeDatabase.Tables) {
		t.Fatal("restore changed exact V27 durable table row counts or canonical bytes")
	}
	for _, table := range []string{"app_auth_throttle", "app_oidc_flows", "app_sessions"} {
		if afterDatabase.Ephemeral[table] != 0 {
			t.Fatalf("restore retained excluded ephemeral authority in %s", table)
		}
	}
	afterObjects := m13RecoveryObjects(t, h, prefixes)
	if !reflect.DeepEqual(afterObjects, beforeObjects) {
		t.Fatal("restore changed selected raw, normalized or archive object bytes/metadata")
	}
	m13RecoveryVerifyReferences(t, h.services.db, h.services.cfg.Schema, afterObjects)

	beforeReopenLedger := afterDatabase.Tables["app_schema_versions"]
	h.open()
	h.denied(oldAdmin, http.MethodGet, "/api/v1/session", nil, http.StatusUnauthorized, "unauthorized")
	h.admin = h.login(oldAdmin.user.Email, h.password, oldAdmin.workspace)
	if h.json(h.admin, http.MethodGet, "/api/v1/session", nil, http.StatusOK).User.ID != oldAdmin.user.ID {
		t.Fatal("restored local user could not reauthenticate")
	}
	ctx, cancel := context.WithTimeout(h.services.ctx, 10*time.Second)
	ok(t, "reopen restored report worker path", h.app.ProcessReports(ctx))
	cancel()
	retention := openRetentionWorker(t, h)
	worked, err := retention.ProcessNext(h.services.ctx)
	ok(t, "reopen restored retention worker", err)
	if worked {
		t.Fatal("restored retention worker found invented work after verified restore")
	}
	ok(t, "close restored retention worker", retention.Close())
	verification, err := Production.OpenVerificationWorker(h.services.ctx, VerificationWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: "aspm-verification", MaxConnections: 1,
		WorkerID: nonce(t) + nonce(t)[:8], LeaseDuration: 2 * time.Second,
		AuthorizationInterval: 25 * time.Millisecond, MaxFixtureBytes: 64 << 10,
		Now: h.services.cfg.Now, LogOutput: io.Discard,
	})
	ok(t, "reopen restored verification worker", err)
	worked, err = verification.ProcessNext(h.services.ctx)
	ok(t, "process restored queued verification job", err)
	if !worked {
		t.Fatal("restored verification worker did not reopen acknowledged queued work")
	}
	ok(t, "close restored verification worker", verification.Close())
	settled := getVerification(t, h, h.admin, primary.ID, queuedVerification.ID, http.StatusOK).Verification
	if settled.State != "succeeded" || settled.Result == nil ||
		settled.Result.CloseFinding || settled.Result.FalsePositive {
		t.Fatal("restored verification worker did not retain bounded current authority")
	}
	asset := h.asset(h.admin, "Post-restore ordinary repository", &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "m13-post-restore", "m13-post-restore-1"
	run := h.finish(h.upload(input).ID, "succeeded")
	if finding := retentionFindingForRun(t, h, run.RunID); finding.ID == "" {
		t.Fatal("new ordinary post-restore import did not create a durable finding")
	}
	finalLedger := m13RecoveryDatabaseSnapshot(t, h.services.db, h.services.cfg.Schema).Tables["app_schema_versions"]
	if finalLedger != beforeReopenLedger {
		t.Fatal("restored core/workers repeated, skipped or rewrote the V27 migration ledger")
	}
}
