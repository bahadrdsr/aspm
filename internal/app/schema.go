package app

import (
	"context"
	"errors"
	"strings"
)

func (a *database) migrate(ctx context.Context) error {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, a.schema).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("application schema must already exist")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aspm/app/migrate/"+a.schema); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+a.table("schema_versions")+` (version integer PRIMARY KEY)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM `+a.table("schema_versions")).Scan(&version); err != nil {
		return err
	}
	if version > 5 {
		return errors.New("application schema is newer than this binary")
	}
	if version == 0 {
		ddl := schemaV1
		for _, name := range []string{"users", "workspaces", "memberships", "bootstrap", "sessions", "auth_throttle", "assets", "imports", "coverage", "findings", "observations", "notes"} {
			ddl = strings.ReplaceAll(ddl, "{{"+name+"}}", a.table(name))
		}
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("schema_versions")+` (version) VALUES (1)`); err != nil {
			return err
		}
	}
	if version < 2 {
		ddl := schemaV2
		for _, name := range []string{"report_snapshots", "workspaces", "memberships", "imports"} {
			ddl = strings.ReplaceAll(ddl, "{{"+name+"}}", a.table(name))
		}
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("schema_versions")+` (version) VALUES (2)`); err != nil {
			return err
		}
	}
	if version < 3 {
		ddl := schemaV3
		for _, name := range []string{"users", "workspaces", "oidc_identities", "oidc_flows"} {
			ddl = strings.ReplaceAll(ddl, "{{"+name+"}}", a.table(name))
		}
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("schema_versions")+` (version) VALUES (3)`); err != nil {
			return err
		}
	}
	if version < 4 {
		ddl := schemaV4
		for _, name := range []string{"imports", "users"} {
			ddl = strings.ReplaceAll(ddl, "{{"+name+"}}", a.table(name))
		}
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("schema_versions")+` (version) VALUES (4)`); err != nil {
			return err
		}
	}
	if version < 5 {
		ddl := schemaV5
		for _, name := range []string{"integration_connections", "finding_deliveries", "workspaces", "users"} {
			ddl = strings.ReplaceAll(ddl, "{{"+name+"}}", a.table(name))
		}
		if _, err = tx.Exec(ctx, ddl); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("schema_versions")+` (version) VALUES (5)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const schemaV1 = `
CREATE TABLE {{users}} (
 id text PRIMARY KEY, name text NOT NULL, email text NOT NULL UNIQUE,
 password_hash text NOT NULL, created_at timestamptz NOT NULL,
 CHECK (email=lower(email))
);
CREATE TABLE {{workspaces}} (
 id text PRIMARY KEY, name text NOT NULL, created_at timestamptz NOT NULL
);
CREATE TABLE {{memberships}} (
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 user_id text NOT NULL REFERENCES {{users}}(id),
 role text NOT NULL CHECK (role IN ('admin','analyst','viewer')),
 PRIMARY KEY(workspace_id,user_id)
);
CREATE TABLE {{bootstrap}} (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 user_id text NOT NULL REFERENCES {{users}}(id),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 enrolled_at timestamptz NOT NULL
);
CREATE TABLE {{sessions}} (
 token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
 user_id text NOT NULL REFERENCES {{users}}(id),
 created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE INDEX app_sessions_user_idx ON {{sessions}}(user_id,expires_at);
CREATE TABLE {{auth_throttle}} (
 bucket integer PRIMARY KEY CHECK (bucket BETWEEN 0 AND 4095),
 window_at timestamptz NOT NULL, attempts integer NOT NULL
);
CREATE TABLE {{assets}} (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 name text NOT NULL, kind text NOT NULL, environment text NOT NULL,
 criticality text NOT NULL, tags text[] NOT NULL DEFAULT '{}', owner_id text,
 created_at timestamptz NOT NULL,
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,owner_id) REFERENCES {{memberships}}(workspace_id,user_id)
);
CREATE INDEX app_assets_workspace_idx ON {{assets}}(workspace_id,id);
CREATE TABLE {{imports}} (
 id text PRIMARY KEY, run_id text NOT NULL UNIQUE, workspace_id text NOT NULL,
 asset_id text NOT NULL, source_id text NOT NULL, scan_id text NOT NULL,
 scope_id text NOT NULL, scope_revision text NOT NULL, scope_branch text NOT NULL,
 format text NOT NULL, mapping jsonb NOT NULL,
 source_scan_at timestamptz, collected_at timestamptz NOT NULL,
 imported_at timestamptz NOT NULL,
 source_status text NOT NULL CHECK (source_status IN ('succeeded','failed')),
 scan_kind text NOT NULL CHECK (scan_kind IN ('full','delta')),
 completeness text NOT NULL CHECK (completeness IN ('complete','partial','unknown')),
 report_digest text NOT NULL CHECK (report_digest ~ '^sha256:[0-9a-f]{64}$'),
 report_key text NOT NULL UNIQUE, report_size bigint NOT NULL CHECK(report_size>=0),
 metadata_digest text NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','processing','succeeded','failed')),
 observation_count integer NOT NULL DEFAULT 0 CHECK(observation_count>=0),
 failure_code text, failure_message text,
 worker_id text, fence bigint NOT NULL DEFAULT 0, attempts integer NOT NULL DEFAULT 0,
 lease_until timestamptz, available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,source_id,scope_id,scope_revision,scope_branch,scan_id),
 UNIQUE(workspace_id,id), UNIQUE(workspace_id,run_id),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES {{assets}}(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX app_imports_claim_idx ON {{imports}}(available_at,id)
 WHERE state IN ('queued','processing');
CREATE TABLE {{coverage}} (
 workspace_id text NOT NULL, asset_id text NOT NULL, source_id text NOT NULL,
 scope_id text NOT NULL, scope_revision text NOT NULL, scope_branch text NOT NULL,
 complete_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES {{assets}}(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE {{findings}} (
 id text PRIMARY KEY, workspace_id text NOT NULL, asset_id text NOT NULL,
 source_id text NOT NULL, scope_id text NOT NULL, scope_revision text NOT NULL,
 scope_branch text NOT NULL, identity_key text NOT NULL,
 title text NOT NULL, description text NOT NULL, remediation text NOT NULL,
 severity text NOT NULL CHECK(severity IN ('critical','high','medium','low','info')),
 evidence_text text NOT NULL, source_label text NOT NULL,
 source_scan_at timestamptz, collected_at timestamptz NOT NULL, imported_at timestamptz NOT NULL,
 source_freshness_at timestamptz,
 source_state text NOT NULL CHECK(source_state IN ('observed','unknown','stale','inferred-resolved')),
 owner_id text, workflow_state text NOT NULL DEFAULT 'open'
 CHECK(workflow_state IN ('open','in-progress','resolved')),
 disposition text NOT NULL DEFAULT 'none' CHECK(disposition IN ('none','accepted-risk')),
 accepted_risk_expires_at timestamptz,
 UNIQUE(workspace_id,id),
 UNIQUE(workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES {{assets}}(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,owner_id) REFERENCES {{memberships}}(workspace_id,user_id)
);
CREATE INDEX app_findings_workspace_idx ON {{findings}}(workspace_id,id);
CREATE TABLE {{observations}} (
 id text PRIMARY KEY, workspace_id text NOT NULL, finding_id text NOT NULL,
 run_id text NOT NULL, ordinal integer NOT NULL, data jsonb NOT NULL,
 UNIQUE(run_id,ordinal),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,run_id) REFERENCES {{imports}}(workspace_id,run_id) ON DELETE CASCADE
);
CREATE INDEX app_observations_finding_idx ON {{observations}}(workspace_id,finding_id,run_id,ordinal);
CREATE TABLE {{notes}} (
 id text PRIMARY KEY, workspace_id text NOT NULL, finding_id text NOT NULL,
 author_id text NOT NULL, text text NOT NULL, created_at timestamptz NOT NULL,
 FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,author_id) REFERENCES {{memberships}}(workspace_id,user_id)
);
CREATE INDEX app_notes_finding_idx ON {{notes}}(workspace_id,finding_id,created_at,id);
`
