package acceptance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const m13RecoveryAPIVersion = "aspm.dev/recovery/v1alpha1"

var m13RecoveryDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var m13RecoveryHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type m13RecoveryPrefix struct {
	Purpose string `json:"purpose"`
	Prefix  string `json:"prefix"`
}

type m13RecoveryManifest struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	FormatVersion int    `json:"formatVersion"`
	BackupID      string `json:"backupId"`
	CreatedAt     string `json:"createdAt"`
	CompletedAt   string `json:"completedAt"`
	Source        struct {
		InstanceID     string `json:"instanceId"`
		ReleaseVersion string `json:"releaseVersion"`
		TargetKind     string `json:"targetKind"`
		Database       struct {
			Name              string   `json:"name"`
			Schema            string   `json:"schema"`
			SchemaVersion     int      `json:"schemaVersion"`
			Ledger            []int    `json:"ledger"`
			ExcludedTableData []string `json:"excludedTableData"`
		} `json:"database"`
		Storage struct {
			Bucket   string              `json:"bucket"`
			Region   string              `json:"region"`
			Prefixes []m13RecoveryPrefix `json:"prefixes"`
		} `json:"storage"`
	} `json:"source"`
	CredentialBoundary struct {
		ActiveSessionsRestored           bool   `json:"activeSessionsRestored"`
		ApplicationCiphertextIncluded    bool   `json:"applicationCiphertextIncluded"`
		IntegrationEncryptionKeyIncluded bool   `json:"integrationEncryptionKeyIncluded"`
		RuntimeSecretsIncluded           bool   `json:"runtimeSecretsIncluded"`
		OperatorAction                   string `json:"operatorAction"`
	} `json:"credentialBoundary"`
	Consistency struct {
		Mode                        string `json:"mode"`
		DistributedAtomic           bool   `json:"distributedAtomic"`
		ApplicationConnections      int    `json:"applicationConnections"`
		TransitionalRows            int    `json:"transitionalRows"`
		DatabaseSnapshotStartedAt   string `json:"databaseSnapshotStartedAt"`
		DatabaseSnapshotCompletedAt string `json:"databaseSnapshotCompletedAt"`
		ObjectCopyStartedAt         string `json:"objectCopyStartedAt"`
		ObjectCopyCompletedAt       string `json:"objectCopyCompletedAt"`
		ObjectRecheckCompletedAt    string `json:"objectRecheckCompletedAt"`
	} `json:"consistency"`
	Tools struct {
		ASPMCTL        string `json:"aspmctl"`
		PostgresServer string `json:"postgresServer"`
		PGDump         string `json:"pgDump"`
		PGRestore      string `json:"pgRestore"`
		PSQL           string `json:"psql"`
		S3Client       string `json:"s3Client"`
		Checksum       string `json:"checksum"`
	} `json:"tools"`
	Database struct {
		Dump struct {
			Path      string `json:"path"`
			SHA256    string `json:"sha256"`
			SizeBytes int64  `json:"sizeBytes"`
		} `json:"dump"`
		Inventory struct {
			Path       string `json:"path"`
			SHA256     string `json:"sha256"`
			SizeBytes  int64  `json:"sizeBytes"`
			TableCount int    `json:"tableCount"`
		} `json:"inventory"`
	} `json:"database"`
	Objects struct {
		Index struct {
			Path      string `json:"path"`
			SHA256    string `json:"sha256"`
			SizeBytes int64  `json:"sizeBytes"`
		} `json:"index"`
		ObjectCount  int   `json:"objectCount"`
		LogicalBytes int64 `json:"logicalBytes"`
		BlobCount    int   `json:"blobCount"`
		BlobBytes    int64 `json:"blobBytes"`
	} `json:"objects"`
	Limitations []string `json:"limitations"`
}

type m13RecoveryInventoryEntry struct {
	Name             string  `json:"name"`
	Policy           string  `json:"policy"`
	RowCount         *int64  `json:"rowCount,omitempty"`
	CopySHA256       *string `json:"copySha256,omitempty"`
	SourceRowCount   *int64  `json:"sourceRowCount,omitempty"`
	RestoredRowCount *int64  `json:"restoredRowCount,omitempty"`
}

type m13RecoveryInventory struct {
	APIVersion    string                      `json:"apiVersion"`
	Kind          string                      `json:"kind"`
	SchemaVersion int                         `json:"schemaVersion"`
	Tables        []m13RecoveryInventoryEntry `json:"tables"`
}

type m13RecoveryObject struct {
	Purpose     string            `json:"purpose"`
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	SizeBytes   int64             `json:"sizeBytes"`
	SHA256      string            `json:"sha256"`
	BlobPath    string            `json:"blobPath"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
}

var m13RecoveryV27Tables = []string{
	"app_ai_egress_grants",
	"app_ai_policies",
	"app_ai_profiles",
	"app_archive_publications",
	"app_assessment_jobs",
	"app_assessment_previews",
	"app_assessment_quotas",
	"app_assets",
	"app_auth_throttle",
	"app_bootstrap",
	"app_coverage",
	"app_finding_change_events",
	"app_finding_correlation_events",
	"app_finding_correlation_members",
	"app_finding_correlations",
	"app_finding_decision_events",
	"app_finding_deliveries",
	"app_finding_disposition_approvals",
	"app_findings",
	"app_imports",
	"app_integration_connections",
	"app_jira_finding_effects",
	"app_memberships",
	"app_notes",
	"app_notification_policies",
	"app_notification_policy_events",
	"app_notification_policy_revisions",
	"app_observations",
	"app_oidc_flows",
	"app_oidc_identities",
	"app_report_exports",
	"app_report_sla_policies",
	"app_report_sla_policy_revisions",
	"app_report_snapshots",
	"app_retention_holds",
	"app_retention_policies",
	"app_retention_preview_items",
	"app_retention_previews",
	"app_retention_run_items",
	"app_retention_runs",
	"app_schema_versions",
	"app_sessions",
	"app_source_collection_records",
	"app_source_collections",
	"app_source_connections",
	"app_source_repository_assets",
	"app_users",
	"app_verification_approvals",
	"app_verification_evidence",
	"app_verification_jobs",
	"app_work_views",
	"app_workspaces",
}

func m13RecoveryStrictJSON[T any](t *testing.T, data []byte, target *T, label string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("%s is not strict JSON: %v", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("%s contains trailing JSON", label)
	}
}

func m13RecoveryFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func m13RecoveryHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func m13RecoveryLocalPath(t *testing.T, path string) {
	t.Helper()
	if path == "" || filepath.IsAbs(path) || !filepath.IsLocal(filepath.FromSlash(path)) ||
		strings.Contains(path, `\`) || strings.ContainsRune(path, 0) {
		t.Fatalf("portable artifact path is not local and slash-normalized: %q", path)
	}
}

func TestM13RecoveryFixtureManifestIsCanonicalScopedAndSecretFree(t *testing.T) {
	raw := m13RecoveryFixture(t, "m13-recovery-manifest.json")
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Contains(raw[:len(raw)-1], []byte{'\n'}) ||
		bytes.Contains(raw, []byte{'\r'}) {
		t.Fatal("manifest fixture must be one canonical LF-terminated JSON line")
	}
	var manifest m13RecoveryManifest
	m13RecoveryStrictJSON(t, raw, &manifest, "M13 recovery manifest")
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("re-encode manifest: %v", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) {
		t.Fatal("manifest fixture is not canonical typed JSON")
	}
	if manifest.APIVersion != m13RecoveryAPIVersion || manifest.Kind != "BackupManifest" ||
		manifest.FormatVersion != 1 || len(manifest.BackupID) != 32 ||
		manifest.Source.ReleaseVersion != "0.1.0-dev.1" ||
		(manifest.Source.TargetKind != "linux" && manifest.Source.TargetKind != "kubernetes") {
		t.Fatal("manifest lost its versioned backup or selected target identity")
	}
	for _, value := range []string{manifest.CreatedAt, manifest.CompletedAt,
		manifest.Consistency.DatabaseSnapshotStartedAt,
		manifest.Consistency.DatabaseSnapshotCompletedAt,
		manifest.Consistency.ObjectCopyStartedAt,
		manifest.Consistency.ObjectCopyCompletedAt,
		manifest.Consistency.ObjectRecheckCompletedAt} {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || parsed.Location() != time.UTC {
			t.Fatalf("manifest timestamp is not canonical UTC RFC3339Nano: %q", value)
		}
	}
	wantLedger := make([]int, 27)
	for index := range wantLedger {
		wantLedger[index] = index + 1
	}
	if manifest.Source.Database.Name != "aspm" || manifest.Source.Database.Schema != "aspm" ||
		manifest.Source.Database.SchemaVersion != 27 ||
		!reflect.DeepEqual(manifest.Source.Database.Ledger, wantLedger) ||
		!reflect.DeepEqual(manifest.Source.Database.ExcludedTableData,
			[]string{"app_auth_throttle", "app_oidc_flows", "app_sessions"}) {
		t.Fatal("manifest database scope, ledger or explicit reauthentication exclusions changed")
	}
	wantPrefixes := []m13RecoveryPrefix{
		{Purpose: "raw", Prefix: "evidence/"},
		{Purpose: "normalized", Prefix: "normalized/"},
		{Purpose: "archive", Prefix: "archive/"},
	}
	if manifest.Source.Storage.Bucket != "aspm-evidence" ||
		manifest.Source.Storage.Region != "us-east-1" ||
		!reflect.DeepEqual(manifest.Source.Storage.Prefixes, wantPrefixes) {
		t.Fatal("manifest did not retain the exact selected bucket and three disjoint prefixes")
	}
	for index, selected := range manifest.Source.Storage.Prefixes {
		if selected.Prefix == "" || !strings.HasSuffix(selected.Prefix, "/") ||
			strings.HasPrefix(selected.Prefix, "/") || strings.Contains(selected.Prefix, `\`) ||
			strings.Contains(selected.Prefix, "/../") || strings.Contains(selected.Prefix, "/./") {
			t.Fatalf("unsafe selected prefix %q", selected.Prefix)
		}
		for _, prior := range manifest.Source.Storage.Prefixes[:index] {
			if strings.HasPrefix(selected.Prefix, prior.Prefix) ||
				strings.HasPrefix(prior.Prefix, selected.Prefix) {
				t.Fatal("selected recovery prefixes overlap")
			}
		}
	}
	if manifest.CredentialBoundary.ActiveSessionsRestored ||
		!manifest.CredentialBoundary.ApplicationCiphertextIncluded ||
		manifest.CredentialBoundary.IntegrationEncryptionKeyIncluded ||
		manifest.CredentialBoundary.RuntimeSecretsIncluded ||
		!strings.Contains(strings.ToLower(manifest.CredentialBoundary.OperatorAction), "reauthenticate") {
		t.Fatal("manifest weakened the explicit encrypted-data and reauthentication boundary")
	}
	if manifest.Consistency.Mode != "quiesced-single-instance" ||
		manifest.Consistency.DistributedAtomic ||
		manifest.Consistency.ApplicationConnections != 0 ||
		manifest.Consistency.TransitionalRows != 0 {
		t.Fatal("manifest made an online, distributed-atomic or nonquiescent consistency claim")
	}
	if manifest.Tools.ASPMCTL != "0.1.0-dev.1" ||
		manifest.Tools.PostgresServer != "18.6" || manifest.Tools.PGDump != "18.6" ||
		manifest.Tools.PGRestore != "18.6" || manifest.Tools.PSQL != "18.6" ||
		manifest.Tools.S3Client != "github.com/aws/aws-sdk-go-v2/service/s3@v1.113.1" ||
		manifest.Tools.Checksum != "sha256" {
		t.Fatal("manifest did not pin the supported recovery tools")
	}
	for _, artifact := range []struct {
		path, digest string
		size         int64
	}{
		{manifest.Database.Dump.Path, manifest.Database.Dump.SHA256, manifest.Database.Dump.SizeBytes},
		{manifest.Database.Inventory.Path, manifest.Database.Inventory.SHA256, manifest.Database.Inventory.SizeBytes},
		{manifest.Objects.Index.Path, manifest.Objects.Index.SHA256, manifest.Objects.Index.SizeBytes},
	} {
		m13RecoveryLocalPath(t, artifact.path)
		if !m13RecoveryDigest.MatchString(artifact.digest) || artifact.size <= 0 {
			t.Fatal("manifest artifact omitted a valid checksum or byte count")
		}
	}
	if manifest.Database.Inventory.TableCount != len(m13RecoveryV27Tables) ||
		manifest.Objects.ObjectCount != 2 || manifest.Objects.BlobCount != 2 ||
		manifest.Objects.LogicalBytes != 66 || manifest.Objects.BlobBytes != 66 {
		t.Fatal("manifest aggregate counts do not describe the selected fixture")
	}
	wantLimits := []string{
		"quiesced-single-instance-only",
		"no-distributed-atomicity",
		"same-release-schema-v27-only",
		"no-ha-or-online-multi-replica",
		"no-point-in-time-recovery",
		"no-cross-target-or-name-remap",
		"no-object-version-history",
		"no-cloud-snapshot-certification",
		"no-rto-rpo-or-throughput-claim",
	}
	if !reflect.DeepEqual(manifest.Limitations, wantLimits) {
		t.Fatal("manifest limitations were broadened, reordered or omitted")
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"postgres://", "postgresql://", `"databaseurl"`, `"accesskey"`, `"secretkey"`,
		`"password"`, `"cookie"`, `"environment"`, `c:\`, `/home/`, `/users/`,
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("portable manifest exposed forbidden private or host-specific material %q", forbidden)
		}
	}

	inventoryRaw := m13RecoveryFixture(t, "m13-recovery-database-inventory.json")
	if manifest.Database.Inventory.SizeBytes != int64(len(inventoryRaw)) ||
		manifest.Database.Inventory.SHA256 != m13RecoveryHash(inventoryRaw) {
		t.Fatal("manifest does not bind the exact database inventory bytes")
	}
	var inventory m13RecoveryInventory
	m13RecoveryStrictJSON(t, inventoryRaw, &inventory, "M13 database inventory")
	inventoryCanonical, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("re-encode database inventory: %v", err)
	}
	inventoryCanonical = append(inventoryCanonical, '\n')
	if !bytes.Equal(inventoryRaw, inventoryCanonical) {
		t.Fatal("database inventory fixture is not canonical typed JSON")
	}
	if inventory.APIVersion != m13RecoveryAPIVersion ||
		inventory.Kind != "DatabaseInventory" || inventory.SchemaVersion != 1 ||
		len(inventory.Tables) != len(m13RecoveryV27Tables) {
		t.Fatal("database inventory version or exact V27 table count changed")
	}
	names := make([]string, 0, len(inventory.Tables))
	for _, table := range inventory.Tables {
		names = append(names, table.Name)
		switch table.Policy {
		case "exact":
			if table.RowCount == nil || *table.RowCount < 0 || table.CopySHA256 == nil ||
				!m13RecoveryDigest.MatchString(*table.CopySHA256) ||
				table.SourceRowCount != nil || table.RestoredRowCount != nil {
				t.Fatalf("exact table inventory is incomplete for %s", table.Name)
			}
		case "schema-only":
			if table.SourceRowCount == nil || *table.SourceRowCount < 0 ||
				table.RestoredRowCount == nil || *table.RestoredRowCount != 0 ||
				table.RowCount != nil || table.CopySHA256 != nil {
				t.Fatalf("schema-only table inventory is incomplete for %s", table.Name)
			}
		default:
			t.Fatalf("unknown table restore policy %q", table.Policy)
		}
	}
	if !reflect.DeepEqual(names, m13RecoveryV27Tables) {
		t.Fatal("database inventory did not list the exact sorted V27 application catalog")
	}
	for _, name := range []string{"app_auth_throttle", "app_oidc_flows", "app_sessions"} {
		index := slices.IndexFunc(inventory.Tables, func(value m13RecoveryInventoryEntry) bool {
			return value.Name == name
		})
		if index < 0 || inventory.Tables[index].Policy != "schema-only" {
			t.Fatalf("%s must force reauthentication instead of restoring ephemeral rows", name)
		}
	}

	indexRaw := m13RecoveryFixture(t, "m13-recovery-objects.jsonl")
	if manifest.Objects.Index.SizeBytes != int64(len(indexRaw)) ||
		manifest.Objects.Index.SHA256 != m13RecoveryHash(indexRaw) {
		t.Fatal("manifest does not bind the exact object index bytes")
	}
	scanner := bufio.NewScanner(bytes.NewReader(indexRaw))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var objects []m13RecoveryObject
	var prior string
	var total int64
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var object m13RecoveryObject
		m13RecoveryStrictJSON(t, line, &object, "M13 object index line")
		encoded, err := json.Marshal(object)
		if err != nil || !bytes.Equal(encoded, line) {
			t.Fatal("object index line is not canonical typed JSON")
		}
		if prior != "" && object.Key <= prior {
			t.Fatal("object index is not strictly sorted by bytewise key")
		}
		prior = object.Key
		if object.Bucket != manifest.Source.Storage.Bucket || object.SizeBytes < 0 ||
			!m13RecoveryDigest.MatchString(object.SHA256) {
			t.Fatal("object index entry lost selected bucket, size or digest")
		}
		digestHex := strings.TrimPrefix(object.SHA256, "sha256:")
		if !m13RecoveryHex.MatchString(digestHex) ||
			object.BlobPath != "objects/sha256/"+digestHex {
			t.Fatal("object index blob is not local and content-addressed")
		}
		m13RecoveryLocalPath(t, object.BlobPath)
		var selected string
		for _, prefix := range manifest.Source.Storage.Prefixes {
			if prefix.Purpose == object.Purpose {
				selected = prefix.Prefix
			}
		}
		if selected == "" || !strings.HasPrefix(object.Key, selected) ||
			strings.Contains(object.Key, `\`) || strings.Contains(object.Key, "/../") ||
			strings.Contains(object.Key, "/./") {
			t.Fatal("object index escaped its selected purpose prefix")
		}
		if len(object.Metadata) != 1 ||
			object.Metadata["sha256"] != digestHex {
			t.Fatal("object index admitted unsupported or inconsistent user metadata")
		}
		switch object.ContentType {
		case "application/json", "application/octet-stream":
		default:
			t.Fatal("object index admitted an unsupported content type")
		}
		total += object.SizeBytes
		objects = append(objects, object)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read object index: %v", err)
	}
	if len(objects) != manifest.Objects.ObjectCount || total != manifest.Objects.LogicalBytes {
		t.Fatal("object index count or logical byte total differs from the manifest")
	}
}

func TestM13RecoverySupportedArtifactsRemainSingleInstanceAndManual(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}
	containerfile := read("Containerfile")
	if !strings.Contains(containerfile, "go build -trimpath -o /out/aspmctl ./cmd/aspmctl") {
		t.Fatal("supported image no longer packages the shared FOSS aspmctl binary")
	}
	values := read("deploy/helm/aspm/values.yaml")
	storage := read("deploy/helm/aspm/templates/storage.yaml")
	application := read("deploy/helm/aspm/templates/application.yaml")
	for _, value := range []string{
		"image: postgres:18.6-alpine",
		"image: chrislusf/seaweedfs:4.47",
		"schema: aspm",
		"bucket: aspm-evidence",
		"prefix: evidence/",
	} {
		if !strings.Contains(values, value) {
			t.Fatalf("Helm production target omitted selected recovery input %q", value)
		}
	}
	if strings.Count(storage, "kind: StatefulSet") != 2 ||
		strings.Count(storage, "replicas: 1") != 2 ||
		!strings.Contains(application, "replicas: {{ $config.replicas }}") {
		t.Fatal("Helm recovery assumptions no longer describe one PostgreSQL/storage instance and explicitly scalable application roles")
	}
	helmLower := strings.ToLower(values + storage + application)
	for _, forbidden := range []string{"volumesnapshot", "cronjob", "backup hook", "cloud snapshot"} {
		if strings.Contains(helmLower, forbidden) {
			t.Fatalf("Helm artifacts introduced an unreviewed recovery mechanism %q", forbidden)
		}
	}
	postgres := read("deploy/quadlet/aspm-postgres.container")
	objectStore := read("deploy/quadlet/aspm-storage.container")
	if !strings.Contains(postgres, "Image=docker.io/library/postgres:18.6-alpine") ||
		!strings.Contains(objectStore, "Image=docker.io/chrislusf/seaweedfs:4.47") {
		t.Fatal("Quadlet recovery target lost its pinned PostgreSQL or SeaweedFS artifact")
	}
	for _, role := range []string{"core", "ingestion@", "retention", "reports"} {
		unit := read("deploy/quadlet/aspm-" + role + ".container")
		if !strings.Contains(unit, "aspm-postgres.service") ||
			(role != "reports" && !strings.Contains(unit, "aspm-storage.service")) {
			t.Fatalf("Quadlet %s role no longer has explicit dependencies needed by the stop-app/keep-data procedure", role)
		}
		if strings.Contains(strings.ToLower(unit), "backup") {
			t.Fatalf("Quadlet %s role introduced an implicit backup side effect", role)
		}
	}
}

func TestM13RecoveryOperatorDocumentationDefinesSafeWorkflow(t *testing.T) {
	root := filepath.Join("..", "..")
	docPath := filepath.Join(root, "docs", "m13-backup-restore.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("production operator documentation missing: %s", docPath)
	}
	text := strings.ToLower(string(doc))
	for _, required := range []string{
		"technical preview",
		"aspmctl backup plan",
		"aspmctl backup create",
		"aspmctl backup verify",
		"aspmctl restore plan",
		"aspmctl restore apply",
		"aspmctl restore verify",
		"aspmctl restore cleanup",
		"quiesced",
		"no distributed atomicity",
		"postgresql 18.6",
		"seaweedfs 4.47",
		"helm",
		"quadlet",
		"reauthenticate",
		"same logical schema",
		"same logical prefixes",
		"do not start",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("operator documentation omitted required bounded statement %q", required)
		}
	}
	for _, forbidden := range []string{
		"high availability backup",
		"point-in-time recovery is supported",
		"zero downtime",
		"atomic database and object",
		"guaranteed rto",
		"guaranteed rpo",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("operator documentation made unsupported claim %q", forbidden)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !bytes.Contains(bytes.ToLower(readme), []byte("docs/m13-backup-restore.md")) &&
		!bytes.Contains(bytes.ToLower(readme), []byte(`docs\m13-backup-restore.md`)) {
		t.Fatal("README does not point operators to the bounded backup/restore procedure")
	}
}

func TestM13RecoveryContractTextRetainsNoContradictoryClaims(t *testing.T) {
	data, err := os.ReadFile("M13-BACKUP-RESTORE-CONTRACT.txt")
	if err != nil {
		t.Fatalf("read recovery contract: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"distributedAtomic=false",
		"app_sessions",
		"app_oidc_flows",
		"app_auth_throttle",
		"same logical database name, schema name",
		"Never use --clean",
		"partial schema or object set is not ready",
		"distinct-cleanup-plan-id",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("acceptance contract omitted corrected boundary %q", required)
		}
	}
	if strings.ContainsRune(text, '\u2014') {
		t.Fatal("acceptance contract contains forbidden punctuation")
	}
}

func Example_m13RecoveryCLI() {
	fmt.Println("aspmctl backup plan --config recovery.json --backup backup-20261009")
	fmt.Println("aspmctl restore plan --config recovery.json --backup backup-20261009 --state restore-state.json")
	// Output:
	// aspmctl backup plan --config recovery.json --backup backup-20261009
	// aspmctl restore plan --config recovery.json --backup backup-20261009 --state restore-state.json
}
