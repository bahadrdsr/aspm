package contracts

import (
	"math"
	"testing"
)

func TestUXActionsAndContext(t *testing.T) {
	root := artifact(t, "ux.contract.json", "UXContract")
	equal(t, root, "status", "hypothesis")
	equal(t, root, "navigation", []string{"work", "assets", "integrations", "reports", "settings"})
	equal(t, root, "stack.foundations", "shadcn/ui")
	equal(t, root, "stack.animatedPrimitives", "animate-ui")
	equal(t, root, "stack.motion", "motion")
	if !contains([]string{"radix", "base-ui"}, textAt(t, root, "stack.primitiveFamily")) {
		t.Error("select one compatible primitive family, not an undocumented mixture")
	}
	budgets := indexed(t, root, "actionBudgets")
	expected := []struct {
		id, context string
		max         float64
		feedback    []string
	}{
		{"inspect-evidence", "finding-visible-in-work-queue", 1, []string{"source-evidence", "freshness"}},
		{"assign-to-me", "authorized-finding-or-selection-visible", 1, []string{"confirmed-result", "safe-undo-when-supported"}},
		{"assign-to-owner", "finding-or-selection-visible", 2, []string{"owner-picker", "confirmed-result"}},
		{"apply-saved-view", "work-screen", 1, []string{"view-applied"}},
		{"create-jira-work-item", "finding-visible-integration-template-configured", 2, []string{"preview", "required-fields", "outbound-approval", "uncertain-delivery"}},
		{"copy-remediation-context", "finding-visible", 1, []string{"permission-check", "clipboard-success-or-failure"}},
		{"bulk-workflow-change", "selection-already-made", 2, []string{"affected-scope", "confirmation", "partial-failures"}},
	}
	ids := []string{}
	for _, want := range expected {
		ids = append(ids, want.id)
	}
	exactIDs(t, budgets, ids...)
	for _, want := range expected {
		t.Run(want.id, func(t *testing.T) {
			budget := budgets[want.id]
			equal(t, budget, "status", "hypothesis")
			equal(t, budget, "startingContext", want.context)
			value := numberAt(t, budget, "maxActivations")
			if value < 1 || value > want.max || math.Trunc(value) != value {
				t.Errorf("maxActivations = %v; want an integer in [1,%v]", value, want.max)
			}
			requireSet(t, budget, "requiredFeedback", false, want.feedback...)
			if want.id == "create-jira-work-item" {
				requireSet(t, budget, "exceptions", true, "required-fields", "policy")
			}
		})
	}
	equal(t, root, "measurement.countPolicy", "in-app-activations")
	requireSet(t, root, "measurement.preconditions", false, "permitted-role", "valid-preconfiguration")
	requireSet(t, root, "measurement.separateMeasures", false,
		"typing", "decision-effort", "external-login-consent", "elapsed-task-time", "browser-work", "api-latency",
	)
	requireSet(t, root, "policyGuards", false,
		"authorization", "required-reason", "expiry", "outbound-data-approval", "destructive-confirmation", "proof-scope-approval",
	)
	requireSet(t, root, "sideEffectPolicy.passiveEvents", false, "hover", "prefetch", "open-detail")
	requireSet(t, root, "sideEffectPolicy.forbiddenEffects", false, "create-external-work-item", "run-active-verification", "send-to-ai")
	requireSet(t, root, "contextPreservation.fields", true, "filters", "sort", "scroll", "selection", "focus")
	requireFlags(t, root, []string{
		"contextPreservation.directLinksWithoutPriorTable", "contextPreservation.smallScreenFullPage",
		"contextPreservation.ignoreStaleResponses", "contextPreservation.stableTargetsDuringRefresh",
		"contextPreservation.stableLayout", "selection.distinguishPageFromAllMatches",
	}, true)
}

func TestUXMotionAccessibilityAndStates(t *testing.T) {
	root := artifact(t, "ux.contract.json", "UXContract")
	for _, token := range []struct {
		path     string
		min, max float64
	}{
		{"motion.tokens.microMs", 80, 120},
		{"motion.tokens.transitionMs", 150, 220},
		{"motion.tokens.panelMs", 150, 300},
	} {
		value := numberAt(t, root, token.path)
		if value < token.min || value > token.max {
			t.Errorf("%s = %v, want [%v,%v] ms", token.path, value, token.min, token.max)
		}
	}
	requireFlags(t, root, []string{
		"motion.respectSystemPreference", "motion.includesCSSAndCopiedComponents",
		"motion.focusIndependentOfAnimation", "motion.operationsIndependentOfAnimation",
		"motion.truthfulProgress", "motion.criticalValuesNotTweened", "motion.boundedAnimationInstances",
	}, true)
	requireSet(t, root, "motion.reducedMotionDisables", false, "nonessential-movement", "stagger", "autoplay")
	requireSet(t, root, "motion.reducedMotionPreserves", false, "operations", "focus", "status")
	equal(t, root, "accessibility.target", "WCAG-2.2-AA")
	equal(t, root, "accessibility.conformanceClaim", "target-not-audited")
	requireSet(t, root, "accessibility.requiredScenarios", false,
		"keyboard-only", "meaningful-labels", "visible-focus", "unobscured-focus", "focus-restoration",
		"contrast", "target-size-spacing", "zoom", "small-screen-details", "status-not-color-only",
	)
	requireSet(t, root, "themes", true, "light", "dark")
	requireSet(t, root, "screenStates", false, "empty", "loading", "stale", "failed", "partial-data", "permission-denied", "success")
	requireFlags(t, root, []string{
		"feedback.actionablePersistentFailures", "feedback.explicitRemotePending",
		"feedback.noDemoDataOnFailure", "feedback.noFakeSuccess", "feedback.untrustedContentRenderedSafely",
	}, true)
	equal(t, root, "localFeedbackBudget.status", "hypothesis")
	equal(t, root, "localFeedbackBudget.excludesNetwork", true)
	value := numberAt(t, root, "localFeedbackBudget.maxMs")
	if value <= 0 || value > 100 {
		t.Errorf("local feedback hypothesis = %v ms, want (0,100]", value)
	}
	for _, field := range []string{"cpu", "browser", "browserVersion"} {
		textAt(t, root, "measurement.referenceDevice."+field)
	}
	for _, field := range []string{"memoryMiB", "viewportWidthPx", "viewportHeightPx", "zoomPercent"} {
		if numberAt(t, root, "measurement.referenceDevice."+field) <= 0 {
			t.Errorf("reference device %s must be explicit and positive", field)
		}
	}
	equal(t, root, "measurement.largeList.paginationOrAccessibleWindowing", true)
	rows := numberAt(t, root, "measurement.largeList.datasetRows")
	rendered := numberAt(t, root, "measurement.largeList.maxRenderedRows")
	if rows < 10000 || rendered <= 0 || rendered >= rows {
		t.Error("declare a representative large dataset and a smaller, bounded accessible render window")
	}
	equal(t, root, "measurement.ingestionRunsConcurrently", true)
}
