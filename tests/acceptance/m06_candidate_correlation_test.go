//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func candidateSARIF(t *testing.T, guid, title, uri string, line int) []byte {
	t.Helper()
	return sarif(t, func(run, result object) {
		driver := run["tool"].(map[string]any)["driver"].(map[string]any)
		driver["rules"].([]any)[0].(map[string]any)["shortDescription"] = object{"text": title}
		result["guid"] = guid
		result["message"] = object{"text": title + " source evidence."}
		location := result["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
		location["artifactLocation"] = object{"uri": uri}
		location["region"] = object{"startLine": line}
	})
}

func activeCorrelationMembers(correlation findingCorrelation) []findingCorrelationMember {
	return slices.DeleteFunc(slices.Clone(correlation.Members), func(member findingCorrelationMember) bool {
		return !member.Active
	})
}

func TestM06_BoundedCandidatesAndMultiMemberCorrelationPreserveFifteenObservations(t *testing.T) {
	h := newHarness(t, true)
	analyst, viewer := h.addUser(h.admin, "analyst"), h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	asset := h.asset(h.admin, "Bounded correlation repository", nil)
	otherAsset := h.asset(h.admin, "Different candidate repository", nil)
	const candidateURI = "src/shared-candidate.go"

	type sourceFixture struct {
		source, guid, title string
		report              []byte
		runs                []imported
		finding             finding
	}
	sources := []*sourceFixture{
		{source: "candidate-source-a", guid: "11111111-1111-4111-8111-111111111111", title: "Scanner A exact location"},
		{source: "candidate-source-b", guid: "22222222-2222-4222-8222-222222222222", title: "Scanner B exact location"},
		{source: "candidate-source-c", guid: "33333333-3333-4333-8333-333333333333", title: "Scanner C exact location"},
	}
	for sourceIndex, source := range sources {
		source.report = candidateSARIF(t, source.guid, source.title, candidateURI, 42)
		for runIndex := 0; runIndex < 5; runIndex++ {
			input := h.input(asset.ID, "sarif", source.report)
			input["sourceId"] = source.source
			input["scanId"] = fmt.Sprintf("%s-scan-%d", source.source, runIndex+1)
			observed := sourceTime.Add(time.Duration(sourceIndex*10+runIndex) * time.Hour)
			input["sourceScanAt"], input["collectedAt"] = observed, observed.Add(time.Minute)
			source.runs = append(source.runs, h.finish(h.upload(input).ID, "succeeded"))
		}
		source.finding = retentionFindingForRun(t, h, source.runs[0].RunID)
		equal(t, source.source+" retains five source observations", len(source.finding.Observations), 5)
	}

	sameSourceInput := h.input(asset.ID, "sarif",
		candidateSARIF(t, "44444444-4444-4444-8444-444444444444", "Same source distractor", candidateURI, 42))
	sameSourceInput["sourceId"], sameSourceInput["scanId"] = sources[0].source, "same-source-distractor"
	sameSource := h.finish(h.upload(sameSourceInput).ID, "succeeded")
	sameSourceFinding := retentionFindingForRun(t, h, sameSource.RunID)

	differentLineInput := h.input(asset.ID, "sarif",
		candidateSARIF(t, "55555555-5555-4555-8555-555555555555", "Different line distractor", candidateURI, 43))
	differentLineInput["sourceId"], differentLineInput["scanId"] = "candidate-source-d", "different-line-distractor"
	differentLine := h.finish(h.upload(differentLineInput).ID, "succeeded")
	differentLineFinding := retentionFindingForRun(t, h, differentLine.RunID)

	differentBranchInput := h.input(asset.ID, "sarif",
		candidateSARIF(t, "77777777-7777-4777-8777-777777777777", "Different branch distractor", candidateURI, 42))
	differentBranchInput["sourceId"], differentBranchInput["scanId"] = "candidate-source-f", "different-branch-distractor"
	differentBranchInput["scope"] = scope{"owned-repository", "1", "refs/heads/feature"}
	differentBranch := h.finish(h.upload(differentBranchInput).ID, "succeeded")
	differentBranchFinding := retentionFindingForRun(t, h, differentBranch.RunID)

	failedInput := h.input(asset.ID, "sarif",
		candidateSARIF(t, "88888888-8888-4888-8888-888888888888", "Failed scan distractor", candidateURI, 42))
	failedInput["sourceId"], failedInput["scanId"], failedInput["sourceStatus"] =
		"candidate-source-g", "failed-scan-distractor", "failed"
	failedRun := h.finish(h.upload(failedInput).ID, "succeeded")
	failedFinding := retentionFindingForRun(t, h, failedRun.RunID)

	otherAssetInput := h.input(otherAsset.ID, "sarif",
		candidateSARIF(t, "66666666-6666-4666-8666-666666666666", "Different asset distractor", candidateURI, 42))
	otherAssetInput["sourceId"], otherAssetInput["scanId"] = "candidate-source-e", "different-asset-distractor"
	otherAssetRun := h.finish(h.upload(otherAssetInput).ID, "succeeded")
	otherAssetFinding := retentionFindingForRun(t, h, otherAssetRun.RunID)

	primary, second, third := sources[0].finding, sources[1].finding, sources[2].finding
	h.denied(foreign, "GET", "/api/v1/findings/"+primary.ID+"/correlation-candidates",
		nil, 404, "not-found")
	h.denied(viewer, "GET", "/api/v1/findings/"+primary.ID+"/correlation-candidates?limit=101",
		nil, 400, "invalid-input")
	firstPage := h.json(viewer, "GET",
		"/api/v1/findings/"+primary.ID+"/correlation-candidates?limit=1", nil, 200).CorrelationCandidates
	equal(t, "candidate page is explicitly bounded", len(firstPage.Items), 1)
	if firstPage.NextCursor == nil {
		t.Fatal("bounded candidate page omitted its continuation")
	}
	secondPage := h.json(viewer, "GET",
		"/api/v1/findings/"+primary.ID+"/correlation-candidates?limit=1&cursor="+*firstPage.NextCursor,
		nil, 200).CorrelationCandidates
	equal(t, "candidate continuation has one item", len(secondPage.Items), 1)
	equal(t, "candidate continuation is terminal", secondPage.NextCursor, (*string)(nil))
	candidates := append(firstPage.Items, secondPage.Items...)
	candidateIDs := []string{candidates[0].Member.FindingID, candidates[1].Member.FindingID}
	slices.Sort(candidateIDs)
	expectedCandidates := []string{second.ID, third.ID}
	slices.Sort(expectedCandidates)
	equal(t, "exact location candidate identities", candidateIDs, expectedCandidates)
	for _, candidate := range candidates {
		equal(t, "candidate match kind", candidate.Match.Kind, "exact-location")
		equal(t, "candidate match branch", candidate.Match.Branch, "refs/heads/main")
		equal(t, "candidate match URI", candidate.Match.URI, candidateURI)
		equal(t, "candidate match line", candidate.Match.Line, 42)
	}
	for _, excluded := range []string{
		sameSourceFinding.ID, differentLineFinding.ID, differentBranchFinding.ID,
		failedFinding.ID, otherAssetFinding.ID,
	} {
		if slices.Contains(candidateIDs, excluded) {
			t.Fatalf("bounded candidates included excluded finding %s", excluded)
		}
	}
	findingsTable := pgx.Identifier{h.services.cfg.Schema, "app_findings"}.Sanitize()
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin bounded candidate plan transaction", err)
	defer tx.Rollback(h.services.ctx)
	_, err = tx.Exec(h.services.ctx, `SET LOCAL enable_seqscan=off`)
	ok(t, "prefer candidate index for bounded plan observation", err)
	explain, err := tx.Query(h.services.ctx, `EXPLAIN (COSTS OFF) SELECT id FROM `+findingsTable+`
		WHERE workspace_id=$1 AND asset_id=$2 AND scope_branch=$3 AND candidate_uri=$4 AND candidate_line=$5
		AND id>$6 ORDER BY id LIMIT 101`,
		h.admin.workspace, asset.ID, "refs/heads/main", candidateURI, 42, "")
	ok(t, "explain bounded candidate lookup", err)
	var plan []string
	for explain.Next() {
		var line string
		ok(t, "scan bounded candidate plan", explain.Scan(&line))
		plan = append(plan, line)
	}
	ok(t, "read bounded candidate plan", explain.Err())
	explain.Close()
	if !strings.Contains(strings.Join(plan, "\n"), "app_findings_correlation_candidate_idx") {
		t.Fatalf("candidate query did not use its bounded index: %v", plan)
	}

	firstPreview := h.json(viewer, "POST", "/api/v1/findings/"+primary.ID+"/merge-previews",
		object{"otherFindingId": second.ID}, 200).MergePreview
	if firstPreview.Correlation != nil {
		t.Fatal("first merge preview unexpectedly reported an existing correlation")
	}
	firstMerge := object{
		"otherFindingId":          second.ID,
		"correlationRevision":     0,
		"primaryDecisionRevision": firstPreview.Primary.DecisionRevision,
		"primaryEvidenceRevision": firstPreview.Primary.EvidenceRevision,
		"otherDecisionRevision":   firstPreview.Other.DecisionRevision,
		"otherEvidenceRevision":   firstPreview.Other.EvidenceRevision,
		"decision":                correlationDecision(&analyst.user.ID, "in-progress", "none", nil),
		"rationale":               "Start the reviewed exact-location source group.",
		"idempotencyKey":          "candidate-group-first-merge",
	}
	group := h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", firstMerge, 201).Correlation
	equal(t, "first candidate merge has two active members", len(activeCorrelationMembers(group)), 2)
	equal(t, "first candidate merge collapses one Work row", len(h.work(h.admin, "")), 7)
	equal(t, "first merge replay identity",
		h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", firstMerge, 200).Correlation.ID, group.ID)

	h.denied(viewer, "GET", "/api/v1/findings/"+second.ID+"/correlation-candidates",
		nil, 409, "conflict")
	remaining := h.json(viewer, "GET",
		"/api/v1/findings/"+primary.ID+"/correlation-candidates", nil, 200).CorrelationCandidates
	equal(t, "active primary exposes only one new source candidate", len(remaining.Items), 1)
	equal(t, "remaining candidate is the third source", remaining.Items[0].Member.FindingID, third.ID)

	addPreview := h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merge-previews",
		object{"otherFindingId": third.ID}, 200).MergePreview
	if addPreview.Correlation == nil || addPreview.Correlation.ID != group.ID ||
		len(activeCorrelationMembers(*addPreview.Correlation)) != 2 {
		t.Fatal("add-member preview omitted the current active correlation")
	}
	addInput := object{
		"otherFindingId":          third.ID,
		"correlationRevision":     addPreview.Correlation.Revision,
		"primaryDecisionRevision": addPreview.Primary.DecisionRevision,
		"primaryEvidenceRevision": addPreview.Primary.EvidenceRevision,
		"otherDecisionRevision":   addPreview.Other.DecisionRevision,
		"otherEvidenceRevision":   addPreview.Other.EvidenceRevision,
		"decision":                correlationDecision(&analyst.user.ID, "in-progress", "none", nil),
		"rationale":               "Add the third independently retained exact-location source.",
		"idempotencyKey":          "candidate-group-add-third",
	}
	staleRevision := object{}
	for key, value := range addInput {
		staleRevision[key] = value
	}
	staleRevision["correlationRevision"] = int64(99)
	h.denied(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", staleRevision, 409, "conflict")
	h.json(h.admin, "POST", "/api/v1/findings/"+third.ID+"/notes",
		object{"text": "Concurrent third-source note invalidates the add preview."}, 201)
	h.denied(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", addInput, 409, "conflict")

	addPreview = h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merge-previews",
		object{"otherFindingId": third.ID}, 200).MergePreview
	addInput["correlationRevision"] = addPreview.Correlation.Revision
	addInput["primaryDecisionRevision"] = addPreview.Primary.DecisionRevision
	addInput["primaryEvidenceRevision"] = addPreview.Primary.EvidenceRevision
	addInput["otherDecisionRevision"] = addPreview.Other.DecisionRevision
	addInput["otherEvidenceRevision"] = addPreview.Other.EvidenceRevision
	group = h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", addInput, 201).Correlation
	equal(t, "three active source variants", len(activeCorrelationMembers(group)), 3)
	equal(t, "multi-member correlation revision", group.Revision, int64(2))
	equal(t, "multi-member merge audit count", len(group.Events), 2)
	equal(t, "three-source group leaves one canonical Work row", len(h.work(h.admin, "")), 6)
	equal(t, "add-member replay identity",
		h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/merges", addInput, 200).Correlation.ID, group.ID)

	merged := h.finding(h.admin, primary.ID)
	equal(t, "three sources retain fifteen observations", len(merged.Observations), 15)
	if merged.Correlation == nil || len(activeCorrelationMembers(*merged.Correlation)) != 3 {
		t.Fatal("primary detail omitted the three active source variants")
	}

	releaseSecond := h.json(viewer, "POST", "/api/v1/findings/"+primary.ID+"/split-previews",
		object{"memberFindingId": second.ID}, 200).SplitPreview
	equal(t, "partial split preview sees three active members",
		len(activeCorrelationMembers(releaseSecond.Correlation)), 3)
	releaseSecondInput := object{
		"memberFindingId":         second.ID,
		"correlationRevision":     releaseSecond.Correlation.Revision,
		"primaryDecisionRevision": releaseSecond.Primary.DecisionRevision,
		"primaryEvidenceRevision": releaseSecond.Primary.EvidenceRevision,
		"memberDecisionRevision":  releaseSecond.Member.DecisionRevision,
		"memberEvidenceRevision":  releaseSecond.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(&analyst.user.ID, "in-progress", "none", nil),
		"memberDecision":          correlationDecision(nil, "open", "none", nil),
		"rationale":               "Release only the second source variant.",
		"idempotencyKey":          "candidate-group-release-second",
	}
	h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/notes",
		object{"text": "Concurrent primary note invalidates the first member-release preview."}, 201)
	h.denied(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/splits",
		releaseSecondInput, 409, "conflict")
	releaseSecond = h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/split-previews",
		object{"memberFindingId": second.ID}, 200).SplitPreview
	releaseSecondInput["correlationRevision"] = releaseSecond.Correlation.Revision
	releaseSecondInput["primaryDecisionRevision"] = releaseSecond.Primary.DecisionRevision
	releaseSecondInput["primaryEvidenceRevision"] = releaseSecond.Primary.EvidenceRevision
	releaseSecondInput["memberDecisionRevision"] = releaseSecond.Member.DecisionRevision
	releaseSecondInput["memberEvidenceRevision"] = releaseSecond.Member.EvidenceRevision
	group = h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/splits",
		releaseSecondInput, 201).Correlation
	equal(t, "partial member release keeps correlation active", group.State, "active")
	equal(t, "partial member release keeps two active variants", len(activeCorrelationMembers(group)), 2)
	equal(t, "released member returns to Work", len(h.work(h.admin, "")), 7)
	for _, member := range group.Members {
		if member.FindingID == second.ID && member.Active {
			t.Fatal("released second source remained active")
		}
	}

	releaseThird := h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/split-previews",
		object{"memberFindingId": third.ID}, 200).SplitPreview
	releaseThirdInput := object{
		"memberFindingId":         third.ID,
		"correlationRevision":     releaseThird.Correlation.Revision,
		"primaryDecisionRevision": releaseThird.Primary.DecisionRevision,
		"primaryEvidenceRevision": releaseThird.Primary.EvidenceRevision,
		"memberDecisionRevision":  releaseThird.Member.DecisionRevision,
		"memberEvidenceRevision":  releaseThird.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(nil, "open", "none", nil),
		"memberDecision":          correlationDecision(nil, "open", "none", nil),
		"rationale":               "Release the last secondary and close the source group.",
		"idempotencyKey":          "candidate-group-release-third",
	}
	separated := h.json(h.admin, "POST", "/api/v1/findings/"+primary.ID+"/splits",
		releaseThirdInput, 201).Correlation
	equal(t, "last member release closes the correlation", separated.State, "split")
	equal(t, "closed correlation has no active memberships", len(activeCorrelationMembers(separated)), 0)
	equal(t, "all source-specific issues return to Work", len(h.work(h.admin, "")), 8)
	equal(t, "multi-member event history", len(separated.Events), 4)

	for _, source := range sources {
		finding := h.finding(h.admin, source.finding.ID)
		equal(t, source.source+" restores five own observations", len(finding.Observations), 5)
		for index, run := range source.runs {
			actual := h.request(h.admin, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200).Body.Bytes()
			if !bytes.Equal(actual, source.report) {
				t.Fatalf("%s run %d evidence changed during multi-member correlation", source.source, index+1)
			}
		}
	}
	h.restart()
	equal(t, "multi-member split survives reopen", len(h.work(h.admin, "")), 8)
}
