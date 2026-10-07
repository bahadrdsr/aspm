package app

const schemaV16 = `
ALTER TABLE {{findings}}
 ADD COLUMN candidate_uri text NOT NULL DEFAULT '' CHECK(octet_length(candidate_uri)<=8192),
 ADD COLUMN candidate_line integer NOT NULL DEFAULT 0 CHECK(candidate_line BETWEEN 0 AND 2147483647),
 ADD CONSTRAINT app_findings_candidate_location_check
  CHECK((candidate_uri='' AND candidate_line=0) OR (candidate_uri<>'' AND candidate_line>0));

CREATE INDEX app_findings_correlation_candidate_idx
 ON {{findings}}(workspace_id,asset_id,scope_branch,candidate_uri,candidate_line,id)
 WHERE candidate_uri<>'' AND candidate_line>0;
`
