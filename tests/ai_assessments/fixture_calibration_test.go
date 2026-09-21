//go:build integration && ai_assessments_fixture

package ai_assessments

import (
	"reflect"
	"testing"

	"github.com/bahadrdsr/aspm/internal/providers"
)

func TestAIAssessmentOwnedIntakeNativeAndPublishedV7Fixture(t *testing.T) {
	h := newHarness(t, true)
	finding := h.seed()
	check(t, len(finding.Observations) == 1 && finding.Observations[0].ID != "" && finding.Observations[0].EvidenceDigest != "",
		"real asset/import/API intake fixture did not produce a selected observation")
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	for _, family := range []string{"openai", "azure-foundry", "anthropic", "local"} {
		p, key := h.profile(family, true)
		_, grant := h.approve(p)
		configuration := h.resolved(p, grant)
		text := reviewedContext()
		ref, contextDigest := "fixture-owned-adapter-evidence", digest([]byte(text))
		h.native.armEvidence(p, key, text, ref, contextDigest, "", "ok", "inconclusive", false)
		adapter, err := providers.Open(h.ctx, providers.Config{Profiles: map[string]providers.Profile{p.ID: configuration.Profile},
			Policy: configuration.Policy, Client: h.native.client, MaxAttempts: 1, MaxOutputTokens: 128, MaxResponseBytes: 32 << 10})
		must(t, "construct real existing provider adapter for boundary calibration only", err)
		result, err := adapter.Assess(h.ctx, providers.Request{WorkspaceID: h.admin.Workspace, RunID: "fixture-native-only",
			FindingID: finding.ID, ProfileID: p.ID, Task: validityTask, DataClass: evidenceClass,
			PromptRevision: promptRevision, EvidenceID: ref, EvidenceDigest: contextDigest, EvidenceText: text})
		must(t, "exercise only owned native protocol through the real existing adapter", err)
		check(t, result.Assessment != nil && result.Assessment.Conclusion == "inconclusive" &&
			reflect.DeepEqual(result.Assessment.EvidenceRefs, []string{ref}) && result.Usage.Known,
			"native fixture/calibration did not produce a real adapter result")
	}
	check(t, h.native.count() == 4, "native boundary calibration request count differs")
	h.assertReadonly(before, storage)
	f := newFixture(t)
	baseline := buildPublishedV7(t, f)
	seeded, _ := seedPublishedV7(t, f)
	rows := legacyData(t, f, baseline.LegacyTables)
	check(t, seeded.FindingID != "" && len(rows["findings"]) == 1 && len(rows["observations"]) == 1 &&
		len(rows["ai_profiles"]) == 1 && len(rows["ai_egress_grants"]) == 1 &&
		reflect.DeepEqual(ledger(t, f), []string{"1", "2", "3", "4", "5", "6", "7"}), "pinned V7 legitimate legacy-data fixture failed")
	t.Log("Fixture-only: real PG/API/S3 intake and selected observation, four owned native adapter responses, and populated pinned published-V7 seed PASS. No preview/job/worker/V8 acceptance was executed or claimed.")
}
