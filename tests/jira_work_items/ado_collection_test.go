//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestADOA1AdminConfigurationAndProfileIsolation(t *testing.T) {
	h := newADO(t, true)
	analyst, viewer := h.user("analyst"), h.user("viewer")
	target, token := adoDefaultTarget(), secret(t)
	input := adoSourceInput(target, token)
	h.remember(token)
	h.adoJSON(actor{}, "POST", adoSources, input, 401)
	h.adoJSON(analyst, "POST", adoSources, input, 403)
	h.adoJSON(viewer, "POST", adoSources, input, 403)
	h.request(h.ctx, h.admin, "POST", adoSources, input, 403, "Origin", "https://unapproved.invalid")
	for _, field := range []string{"endpoint", "headers", "requestedBy", "encryptionKey", "repository"} {
		bad := adoSourceInput(target, token)
		bad[field] = "not-caller-authority"
		h.adoJSON(h.admin, "POST", adoSources, bad, 400)
	}
	for _, field := range []string{"token", "enabled", "azureDevOps"} {
		bad := adoSourceInput(target, token)
		delete(bad, field)
		h.adoJSON(h.admin, "POST", adoSources, bad, 400)
	}
	for _, bad := range []adoTarget{
		{"https://dev.azure.com/owned", target.ProjectID, target.RepositoryID},
		{"owned/other", target.ProjectID, target.RepositoryID},
		{" owned", target.ProjectID, target.RepositoryID},
		{target.Organization, "project-name", target.RepositoryID},
		{target.Organization, target.ProjectID, "repository-name"},
	} {
		h.adoJSON(h.admin, "POST", adoSources, adoSourceInput(bad, token), 400)
	}
	upper := adoTarget{strings.ToUpper(target.Organization), strings.ToUpper(target.ProjectID), strings.ToUpper(target.RepositoryID)}
	source := h.adoSource(h.admin, upper, token)
	h.adoEncrypted(source, token)
	body, _ := h.request(h.ctx, viewer, "GET", adoSources+"/"+source.ID, nil, 200)
	shape := decoded[map[string]json.RawMessage](t, decoded[map[string]json.RawMessage](t, body)["source"])
	check(t, len(shape) == 10 && shape["azureDevOps"] != nil && shape["repository"] == nil &&
		shape["token"] == nil && shape["endpoint"] == nil, "ADO source DTO is not profile typed/write-only")
	for _, field := range []string{"profile", "azureDevOps", "repository", "requestedBy"} {
		h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{field: "not-retargetable"}, 400)
	}
	h.adoJSON(viewer, "PATCH", adoSources+"/"+source.ID, object{"enabled": false}, 403)
	h.adoJSON(analyst, "PATCH", adoSources+"/"+source.ID, object{"enabled": false}, 403)
	replacement := secret(t)
	h.remember(replacement)
	changed := h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"token": replacement, "name": "Rotated explicit PAT"}, 200).Source
	check(t, changed.Revision == source.Revision+1, "meaningful source update did not advance revision once")
	h.adoEncrypted(changed, replacement)
	again := h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"token": replacement, "name": changed.Name}, 200).Source
	same(t, "exact configuration replay changed the source", again, changed)
	h.adoSource(h.admin, target, token)

	githubToken := secret(t)
	h.remember(githubToken)
	oldBody, _ := h.request(h.ctx, h.admin, "POST", adoSources, object{
		"profile": "github-cloud-app", "name": "Old GitHub shape", "repository": "owned/legacy",
		"enabled": true, "token": githubToken,
	}, 201)
	github := decoded[adoReply](t, oldBody).Source
	h.adoEncrypted(github, githubToken)
	oldShape := decoded[map[string]json.RawMessage](t, decoded[map[string]json.RawMessage](t, oldBody)["source"])
	check(t, len(oldShape) == 10 && oldShape["repository"] != nil && oldShape["azureDevOps"] == nil,
		"unselected GitHub source acquired ADO fields")
	plain := h.adoJSON(viewer, "GET", adoSources, nil, 200)
	explicit := h.adoJSON(viewer, "GET", adoSources+"?profile=github-cloud-app", nil, 200)
	same(t, "default source list is not byte-equivalent GitHub-only metadata", plain, explicit)
	check(t, plain.Total == 1 && len(plain.Items) == 1, "old UI default list mixed ADO rows into GitHub")
	defaultSource := decoded[adoSource](t, plain.Items[0])
	check(t, defaultSource.ID == github.ID && defaultSource.Profile == "github-cloud-app" &&
		defaultSource.Repository == "owned/legacy" && defaultSource.AzureDevOps == nil, "default page is not the historical selected GitHub profile")
	page := h.adoJSON(viewer, "GET", adoSources+"?profile="+adoProfile+"&limit=1", nil, 200)
	check(t, page.Total == 2 && len(page.Items) == 1 && page.NextCursor != nil, "ADO-only list lost real filtered paging")
	next := h.adoJSON(viewer, "GET", adoSources+"?profile="+adoProfile+"&limit=1&cursor="+*page.NextCursor, nil, 200)
	check(t, len(next.Items) == 1 && next.NextCursor == nil && !bytes.Equal(next.Items[0], page.Items[0]),
		"ADO source continuation repeated or mixed profiles")
	for _, raw := range []json.RawMessage{page.Items[0], next.Items[0]} {
		value := decoded[adoSource](t, raw)
		check(t, value.Profile == adoProfile && value.AzureDevOps != nil && value.Repository == "", "ADO-only page contains another profile")
	}
	for _, query := range []string{"profile=", "profile=unknown", "profile=GITHUB-CLOUD-APP",
		"profile="+adoProfile+"&profile="+adoProfile, "profile=github-cloud-app&profile="+adoProfile} {
		h.adoJSON(viewer, "GET", adoSources+"?"+query, nil, 400)
	}
	foreign := h.otherWorkspace()
	h.adoJSON(foreign, "GET", adoSources+"/"+source.ID, nil, 404)
	check(t, h.adoJSON(foreign, "GET", adoSources+"?profile="+adoProfile, nil, 200).Total == 0, "source target crossed a workspace")
	h.json(h.admin, "PATCH", "/api/v1/users/"+analyst.ID, object{"role": "viewer"}, 200)
	h.adoQueue(analyst, source.ID, adoDefaultSelection(), "revoked-role", 403)
	for _, row := range h.rows("SELECT (to_jsonb(v)-'credential_ciphertext')::text FROM "+h.table("source_connections")+" v") {
		h.private([]byte(row))
	}
	check(t, h.native.calls.Load() == 0 && h.publisher.puts.Load() == 0 && h.raw.puts.Load() == 0,
		"source configuration/listing started collection or intake")
}

func TestADOA2ExactCollectionReplayAssetsAndPartialEvidence(t *testing.T) {
	t.Run("exact-selected-records", func(t *testing.T) {
		h := newADO(t, true)
		writer, viewer := h.user("analyst"), h.user("viewer")
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		script := h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
		h.adoQueue(viewer, source.ID, selection, "viewer", 403)
		for _, field := range []string{"endpoint", "repositoryId", "profile", "token", "requestedBy"} {
			bad := adoQueueInput(selection, "forged-"+field)
			bad[field] = "not-authority"
			h.adoJSON(writer, "POST", adoSources+"/"+source.ID+"/collections", bad, 400)
		}
		for _, bad := range []adoSelection{{"latest", selection.ArtifactName, selection.ArtifactPath},
			{"081", selection.ArtifactName, selection.ArtifactPath}, {selection.BuildID, "all/*", selection.ArtifactPath},
			{selection.BuildID, selection.ArtifactName, "../escape.sarif"},
			{selection.BuildID, selection.ArtifactName, "reports\\report.sarif"}} {
			h.adoQueue(writer, source.ID, bad, "invalid-selection", 400)
		}
		job := h.adoQueue(writer, source.ID, selection, "immutable-selected", 202)
		check(t, job.State == "queued" && job.RequestedBy == writer.ID && job.ConnectionRevision == source.Revision &&
			job.AssetID == nil && job.RecordCount == 0 && !job.Complete, "API did not leave one exact queued collection")
		same(t, "collection lost selected native target", job.AzureDevOps, &target)
		same(t, "collection lost selected build/artifact/path", job.Selection, &selection)
		same(t, "collection exact replay rewrote the job", h.adoQueue(writer, source.ID, selection, "immutable-selected", 200), job)
		for _, changed := range []adoSelection{{"82", selection.ArtifactName, selection.ArtifactPath},
			{selection.BuildID, "other-artifact", selection.ArtifactPath}, {selection.BuildID, selection.ArtifactName, "reports/other.sarif"}} {
			h.adoQueue(writer, source.ID, changed, "immutable-selected", 409)
		}
		h.adoQueue(h.admin, source.ID, selection, "immutable-selected", 409)
		foreign := h.otherWorkspace()
		h.adoQueue(foreign, source.ID, selection, "foreign-source", 404)
		h.adoJSON(foreign, "GET", adoCollectionPath(job.ID), nil, 404)
		h.adoJSON(foreign, "GET", adoRecordsPath(job.ID), nil, 404)
		check(t, h.native.calls.Load() == 0 && h.publisher.puts.Load() == 0, "API dispatched native collection")
		h.reopen()
		worker := h.adoWorker(h.adoWorkerConfig())
		adoStep(t, h.ctx, worker, true)
		done := h.adoJob(viewer, job.ID)
		check(t, done.State == "succeeded" && done.Complete && done.RecordCount == 4 && done.AssetID != nil &&
			done.RepositoryID != nil && *done.RepositoryID == target.RepositoryID && done.CollectedAt != nil &&
			done.Failure == nil && len(done.Gaps) == 0 && h.native.calls.Load() == 4,
			"selected ADO collection did not persist all four scoped feeds")
		records := h.adoRecords(viewer, job.ID)
		want := map[string][]byte{"repository": script.repository, "pipeline": script.build, "artifact": script.artifact, "report": script.report}
		external := map[string]string{"repository": target.RepositoryID, "pipeline": selection.BuildID, "artifact": "9", "report": "9:" + selection.ArtifactPath}
		ordinals := map[string]int{"repository": 0, "pipeline": 1, "artifact": 2, "report": 3}
		check(t, len(records) == 4, "record list omitted a selected native feed")
		for _, record := range records {
			raw, present := want[record.Kind]
			check(t, present && record.Evidence.SHA256 == "sha256:"+digest(raw) &&
				record.Evidence.SizeBytes == int64(len(raw)) && record.SourceScanAt == nil && record.SourceUpdatedAt == nil &&
				record.ExternalID == external[record.Kind] && record.Ordinal == ordinals[record.Kind],
				"native records lost exact bytes/digest or invented scanner freshness")
			if record.Kind != "repository" {
				check(t, record.ParentID == target.RepositoryID && record.NativeRunID == selection.BuildID, "native repository/build identity was replaced by internal IDs")
			}
			data, _ := h.request(h.ctx, viewer, "GET", adoEvidencePath(job.ID, record.ID), nil, 200)
			check(t, bytes.Equal(data, raw), "evidence API rewrote native record bytes")
			ref := h.adoRef(record.ID)
			object, err := h.store.GetObject(h.ctx, &s3.GetObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)})
			must(t, "independently read real published S3 object", err)
			stored, readErr := io.ReadAll(io.LimitReader(object.Body, ref.SizeBytes+1))
			must(t, "read independent bounded S3 bytes", readErr)
			must(t, "close independent S3 body", object.Body.Close())
			check(t, bytes.Equal(stored, raw), "actual S3 object differs from native input")
			u, err := url.Parse(record.RawURL)
			must(t, "inspect native provenance URL", err)
			check(t, u.Scheme+"://"+u.Host == h.native.server.URL && strings.HasPrefix(u.Path, "/"+target.Organization+"/"+target.ProjectID+"/_apis/"),
				"record URL lost approved native origin/org/project")
			base := "/" + target.Organization + "/" + target.ProjectID + "/_apis"
			paths := map[string]string{"repository": base + "/git/repositories/" + target.RepositoryID,
				"pipeline": base + "/build/builds/" + selection.BuildID,
				"artifact": base + "/build/builds/" + selection.BuildID + "/artifacts",
				"report": base + "/build/builds/" + selection.BuildID + "/artifacts"}
			check(t, u.Path == paths[record.Kind] && u.Query().Get("api-version") == "7.1", "record provenance substituted another native feed")
			if record.Kind == "artifact" || record.Kind == "report" {
				check(t, u.Query().Get("artifactName") == selection.ArtifactName, "artifact provenance lost exact selection")
			}
			if record.Kind == "report" {
				check(t, u.Query().Get("$format") == "zip", "report provenance lost its bounded ZIP download")
			}
			h.request(h.ctx, foreign, "GET", adoEvidencePath(job.ID, record.ID), nil, 404)
		}
		asset := h.json(viewer, "GET", "/api/v1/assets/"+*done.AssetID, nil, 200).Asset
		check(t, asset.Kind == "repository" && asset.Name == script.name && asset.Criticality == "medium" &&
			asset.OwnerID == nil && asset.Environment == "" && len(asset.Tags) == 0,
			"untrusted repository metadata assigned ownership or decisions")
		check(t, h.json(viewer, "GET", "/api/v1/work", nil, 200).Total == 0, "collection automatically imported findings")
		for _, table := range []string{"imports", "findings", "observations", "coverage"} {
			same(t, "collection invented scan/canonical business data", h.rows("SELECT 'present' FROM "+h.table(table)+" LIMIT 1"), []string{})
		}
		for _, row := range h.rows("SELECT to_jsonb(v)::text FROM "+h.table("source_collection_records")+" v") {
			check(t, !strings.Contains(row, adoReportCanary), "PG record metadata stored report content instead of its scoped evidence ref")
			h.private([]byte(row))
		}
		h.adoJSON(h.admin, "PATCH", adoSources+"/"+source.ID, object{"name": "Revision changed"}, 200)
		h.adoQueue(writer, source.ID, selection, "immutable-selected", 409)
		same(t, "revision conflict rewrote accepted collection", h.adoJob(viewer, job.ID), done)
		adoStep(t, h.ctx, worker, false)
	})

	t.Run("stable-asset-renamed-report-new-build", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		worker := h.adoWorker(h.adoWorkerConfig())
		h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
		first := h.adoQueue(h.admin, source.ID, selection, "first", 202)
		adoStep(t, h.ctx, worker, true)
		assetID := h.adoJob(h.admin, first.ID).AssetID
		check(t, assetID != nil, "first selected collection did not link an asset")
		human := h.json(h.admin, "PATCH", "/api/v1/assets/"+*assetID, object{
			"name": "Human label", "ownerId": h.admin.ID, "criticality": "critical",
			"environment": "production", "tags": []string{"human", "preserved"},
		}, 200).Asset
		h.reopen()
		secondSource := h.adoSource(h.admin, target, token)
		for i, sourceID := range []string{source.ID, secondSource.ID} {
			next := selection
			next.BuildID = []string{"82", "83"}[i]
			script := h.native.arm(target, next, token, adoSARIF(false, false), "", "")
			script.repository = bytes.ReplaceAll(script.repository, []byte(script.name), []byte("upstream-renamed"))
			if i == 1 {
				script.repository = bytes.ReplaceAll(script.repository, []byte(target.RepositoryID), []byte(strings.ToUpper(target.RepositoryID)))
				script.build = bytes.ReplaceAll(script.build, []byte(target.RepositoryID), []byte(strings.ToUpper(target.RepositoryID)))
			}
			job := h.adoQueue(h.admin, sourceID, next, "next-"+next.BuildID, 202)
			adoStep(t, h.ctx, worker, true)
			value := h.adoJob(h.admin, job.ID)
			check(t, value.AssetID != nil && *value.AssetID == *assetID && value.Selection.BuildID == next.BuildID,
				"new build/connection/rename duplicated stable native repository")
			same(t, "upstream data overwrote human fields", h.json(h.admin, "GET", "/api/v1/assets/"+*assetID, nil, 200).Asset, human)
		}
		check(t, h.json(h.admin, "GET", "/api/v1/assets", nil, 200).Total == 1, "stable target created multiple assets")
		key := target.Organization + "/" + target.ProjectID + "/" + target.RepositoryID
		same(t, "internal stable asset key omitted organization/project or exposed a GUID-only link",
			h.rows("SELECT repository_id FROM "+h.table("source_repository_assets")+" WHERE profile=$1", adoProfile), []string{key})
	})

	for _, distinction := range []string{"organization", "project", "workspace"} {
		t.Run("tenant-separation-"+distinction, func(t *testing.T) {
			h := newADO(t, true)
			target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
			other := target
			if distinction == "organization" {
				other.Organization = "other-org"
			} else if distinction == "project" {
				other.ProjectID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			}
			worker := h.adoWorker(h.adoWorkerConfig())
			var ids []string
			for i, selected := range []adoTarget{target, other} {
				who := h.admin
				if distinction == "workspace" && i == 1 {
					who = h.otherWorkspace()
				}
				source := h.adoSource(who, selected, token)
				h.native.arm(selected, selection, token, adoSARIF(false, false), "", "")
				job := h.adoQueue(who, source.ID, selection, []string{"first", "other"}[i], 202)
				adoStep(t, h.ctx, worker, true)
				value := h.adoJob(who, job.ID)
				check(t, value.AssetID != nil && value.RepositoryID != nil && *value.RepositoryID == target.RepositoryID, "scoped native UUID was lost")
				ids = append(ids, *value.AssetID)
			}
			check(t, ids[0] != ids[1], "same GUID from different org/project/workspace collided")
		})
	}

	t.Run("partial-artifact-unavailable", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		script := h.native.arm(target, selection, token, adoSARIF(false, false), "artifact-missing", "")
		job := h.adoQueue(h.admin, source.ID, selection, "partial", 202)
		adoStep(t, h.ctx, h.adoWorker(h.adoWorkerConfig()), true)
		done := h.adoJob(h.admin, job.ID)
		check(t, done.State == "partial" && !done.Complete && done.RecordCount == 2 && done.AssetID != nil &&
			done.Failure != nil && done.Failure.Code == "unavailable" && len(done.Gaps) > 0 && h.native.calls.Load() == 3,
			"upstream partial failure lost available records or became completed scan")
		for _, record := range h.adoRecords(h.admin, job.ID) {
			want := map[string][]byte{"repository": script.repository, "pipeline": script.build}[record.Kind]
			check(t, want != nil, "partial collection fabricated an artifact/report")
			data, _ := h.request(h.ctx, h.admin, "GET", adoEvidencePath(job.ID, record.ID), nil, 200)
			check(t, bytes.Equal(data, want), "partial raw evidence was discarded or rewritten")
		}
		check(t, h.raw.puts.Load() == 0, "partial collection triggered intake")
	})
}
