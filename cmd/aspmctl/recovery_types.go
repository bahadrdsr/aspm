package main

import "time"

const recoveryAPIVersion = "aspm.dev/recovery/v1alpha1"

type recoveryConfiguration struct {
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

type recoveryPlan struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Operation  string `json:"operation"`
	InstanceID string `json:"instanceId"`
	ReadOnly   bool   `json:"readOnly"`
}

type recoveryReceipt struct {
	APIVersion     string `json:"apiVersion"`
	Kind           string `json:"kind"`
	Phase          string `json:"phase"`
	BackupID       string `json:"backupId"`
	RestoreID      string `json:"restoreId,omitempty"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type recoveryPrefix struct {
	Purpose string `json:"purpose"`
	Prefix  string `json:"prefix"`
}

type recoveryManifest struct {
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
			Bucket   string           `json:"bucket"`
			Region   string           `json:"region"`
			Prefixes []recoveryPrefix `json:"prefixes"`
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

type recoveryInventoryEntry struct {
	Name             string  `json:"name"`
	Policy           string  `json:"policy"`
	RowCount         *int64  `json:"rowCount,omitempty"`
	CopySHA256       *string `json:"copySha256,omitempty"`
	SourceRowCount   *int64  `json:"sourceRowCount,omitempty"`
	RestoredRowCount *int64  `json:"restoredRowCount,omitempty"`
}

type recoveryInventory struct {
	APIVersion    string                   `json:"apiVersion"`
	Kind          string                   `json:"kind"`
	SchemaVersion int                      `json:"schemaVersion"`
	Tables        []recoveryInventoryEntry `json:"tables"`
}

type recoveryObject struct {
	Purpose     string            `json:"purpose"`
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	SizeBytes   int64             `json:"sizeBytes"`
	SHA256      string            `json:"sha256"`
	BlobPath    string            `json:"blobPath"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
}

type recoveryRestoreState struct {
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

type recoveryToolVersions struct {
	Server, PGDump, PGRestore, PSQL string
}

type recoveryObjectSummary struct {
	Objects      []recoveryObject
	ObjectCount  int
	LogicalBytes int64
	BlobCount    int
	BlobBytes    int64
}

type recoveryTiming struct {
	DatabaseStart, DatabaseEnd time.Time
	ObjectStart, ObjectEnd     time.Time
	ObjectRecheck              time.Time
}

var recoveryTables = []string{
	"app_ai_egress_grants", "app_ai_policies", "app_ai_profiles", "app_archive_publications",
	"app_assessment_jobs", "app_assessment_previews", "app_assessment_quotas", "app_assets",
	"app_auth_throttle", "app_bootstrap", "app_coverage", "app_finding_change_events",
	"app_finding_correlation_events", "app_finding_correlation_members", "app_finding_correlations",
	"app_finding_decision_events", "app_finding_deliveries", "app_finding_disposition_approvals",
	"app_findings", "app_imports", "app_integration_connections", "app_jira_finding_effects",
	"app_memberships", "app_notes", "app_notification_policies", "app_notification_policy_events",
	"app_notification_policy_revisions", "app_observations", "app_oidc_flows", "app_oidc_identities",
	"app_report_exports", "app_report_sla_policies", "app_report_sla_policy_revisions",
	"app_report_snapshots", "app_retention_holds", "app_retention_policies",
	"app_retention_preview_items", "app_retention_previews", "app_retention_run_items",
	"app_retention_runs", "app_schema_versions", "app_sessions", "app_source_collection_records",
	"app_source_collections", "app_source_connections", "app_source_repository_assets", "app_users",
	"app_verification_approvals", "app_verification_evidence", "app_verification_jobs",
	"app_work_views", "app_workspaces",
}

var recoveryEphemeral = map[string]bool{
	"app_auth_throttle": true,
	"app_oidc_flows":    true,
	"app_sessions":      true,
}
