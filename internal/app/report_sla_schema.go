package app

const schemaV25 = `
ALTER TABLE {{findings}}
 ADD COLUMN first_observed_at timestamptz;

UPDATE {{findings}} f
SET first_observed_at=COALESCE((
 SELECT min(i.imported_at)
 FROM {{observations}} o JOIN {{imports}} i
 ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
 WHERE o.workspace_id=f.workspace_id AND o.finding_id=f.id
),f.imported_at);

ALTER TABLE {{findings}}
 ALTER COLUMN first_observed_at SET NOT NULL;

CREATE INDEX app_findings_sla_candidate_idx
 ON {{findings}}(workspace_id,workflow_state,severity,first_observed_at,id)
 WHERE workflow_state<>'resolved';

CREATE TABLE {{report_sla_policies}} (
 workspace_id text NOT NULL,
 critical_days integer NOT NULL DEFAULT 7,
 high_days integer NOT NULL DEFAULT 30,
 medium_days integer NOT NULL DEFAULT 90,
 low_days integer NOT NULL DEFAULT 180,
 info_days integer NOT NULL DEFAULT 365,
 revision bigint NOT NULL DEFAULT 1,
 approved_by text,
 approved_by_name text,
 rationale text NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CONSTRAINT app_report_sla_policies_pkey PRIMARY KEY(workspace_id),
 CONSTRAINT app_report_sla_policies_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_report_sla_policies_approved_by_fkey
  FOREIGN KEY(approved_by) REFERENCES {{users}}(id),
 CONSTRAINT app_report_sla_policies_actor_check
  CHECK((approved_by IS NULL)=(approved_by_name IS NULL)),
 CONSTRAINT app_report_sla_policies_revision_check CHECK(revision>0),
 CONSTRAINT app_report_sla_policies_rationale_check
  CHECK(octet_length(rationale)>=1 AND octet_length(rationale)<=8192 AND btrim(rationale)<>''),
 CONSTRAINT app_report_sla_policies_targets_check CHECK(
  critical_days>=1 AND critical_days<=3650 AND
  high_days>=1 AND high_days<=3650 AND
  medium_days>=1 AND medium_days<=3650 AND
  low_days>=1 AND low_days<=3650 AND
  info_days>=1 AND info_days<=3650 AND
  critical_days<=high_days AND high_days<=medium_days AND
  medium_days<=low_days AND low_days<=info_days)
);

CREATE TABLE {{report_sla_policy_revisions}} (
 workspace_id text NOT NULL,
 revision bigint NOT NULL,
 critical_days integer NOT NULL,
 high_days integer NOT NULL,
 medium_days integer NOT NULL,
 low_days integer NOT NULL,
 info_days integer NOT NULL,
 approved_by text,
 approved_by_name text,
 rationale text NOT NULL,
 created_at timestamptz NOT NULL,
 CONSTRAINT app_report_sla_policy_revisions_pkey PRIMARY KEY(workspace_id,revision),
 CONSTRAINT app_report_sla_policy_revisions_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_report_sla_policy_revisions_approved_by_fkey
  FOREIGN KEY(approved_by) REFERENCES {{users}}(id),
 CONSTRAINT app_report_sla_policy_revisions_actor_check
  CHECK((approved_by IS NULL)=(approved_by_name IS NULL)),
 CONSTRAINT app_report_sla_policy_revisions_revision_check CHECK(revision>0),
 CONSTRAINT app_report_sla_policy_revisions_rationale_check
  CHECK(octet_length(rationale)>=1 AND octet_length(rationale)<=8192 AND btrim(rationale)<>''),
 CONSTRAINT app_report_sla_policy_revisions_targets_check CHECK(
  critical_days>=1 AND critical_days<=3650 AND
  high_days>=1 AND high_days<=3650 AND
  medium_days>=1 AND medium_days<=3650 AND
  low_days>=1 AND low_days<=3650 AND
  info_days>=1 AND info_days<=3650 AND
  critical_days<=high_days AND high_days<=medium_days AND
  medium_days<=low_days AND low_days<=info_days)
);

INSERT INTO {{report_sla_policies}}
 (workspace_id,approved_by,approved_by_name,rationale,created_at,updated_at)
SELECT id,NULL,NULL,'Initial default remediation targets.',statement_timestamp(),statement_timestamp()
FROM {{workspaces}}
ON CONFLICT(workspace_id) DO NOTHING;

INSERT INTO {{report_sla_policy_revisions}}
 (workspace_id,revision,critical_days,high_days,medium_days,low_days,info_days,
  approved_by,approved_by_name,rationale,created_at)
SELECT workspace_id,revision,critical_days,high_days,medium_days,low_days,info_days,
 approved_by,approved_by_name,rationale,created_at
FROM {{report_sla_policies}}
ON CONFLICT(workspace_id,revision) DO NOTHING;
`
