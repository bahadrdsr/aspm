package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestM13TechnicalPreviewQualityEvidence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "docs", "evidence", "m13-technical-preview-quality.json"))
	if err != nil {
		t.Fatalf("read M13 quality evidence: %v", err)
	}
	var evidence struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Release       string `json:"release"`
		RecordedOn    string `json:"recordedOn"`
		NativeLinux   struct {
			Status                   string `json:"status"`
			QualifiedRuntimeRevision string `json:"qualifiedRuntimeRevision"`
			Host                     string `json:"host"`
			PodmanVersion            string `json:"podmanVersion"`
			PostgresVersion          string `json:"postgresVersion"`
			SeaweedFSVersion         string `json:"seaweedfsVersion"`
			SignedInstall            bool   `json:"signedInstall"`
			OwnerOnlyInputs          bool   `json:"ownerOnlyInputs"`
			AllRolesReady            bool   `json:"allRolesReady"`
			SchemaLedger             struct {
				Count, Minimum, Maximum int
			} `json:"schemaLedger"`
			RestartPreservedAsset                 bool `json:"restartPreservedAsset"`
			PreActivationUpgradeFailure           bool `json:"preActivationUpgradeFailure"`
			SignedRollbackActivatedPreviousBundle bool `json:"signedRollbackActivatedPreviousBundle"`
			RollbackPreservedAsset                bool `json:"rollbackPreservedAsset"`
		} `json:"nativeLinux"`
		Accessibility struct {
			Status                   string   `json:"status"`
			Target                   string   `json:"target"`
			ConformanceClaim         string   `json:"conformanceClaim"`
			Browser                  string   `json:"browser"`
			Scenarios                []string `json:"scenarios"`
			LocalFeedbackBudgetMs    float64  `json:"localFeedbackBudgetMs"`
			ColdFixtureShellBudgetMs float64  `json:"coldFixtureShellBudgetMs"`
		} `json:"accessibility"`
		Capacity struct {
			Status                    string             `json:"status"`
			Profile                   string             `json:"profile"`
			Findings                  int                `json:"findings"`
			ReportBytes               int                `json:"reportBytes"`
			UploadMs                  float64            `json:"uploadMs"`
			ProcessingSeconds         float64            `json:"processingSeconds"`
			WorkPageRows              int                `json:"workPageRows"`
			WorkTotal                 int                `json:"workTotal"`
			WorkP95Ms                 float64            `json:"workP95Ms"`
			WorkP99Ms                 float64            `json:"workP99Ms"`
			AcknowledgedFindingLoss   int                `json:"acknowledgedFindingLoss"`
			MaximumContainerMemoryMiB float64            `json:"maximumContainerMemoryMiB"`
			ContainerMemoryMiB        map[string]float64 `json:"containerMemoryMiB"`
		} `json:"capacity"`
		Limitations []string `json:"limitations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode M13 quality evidence: %v", err)
	}
	if evidence.SchemaVersion != 1 || evidence.Kind != "TechnicalPreviewQualityEvidence" ||
		evidence.Release != "0.1.0-rc.1" ||
		!regexp.MustCompile(`^2026-10-[0-9]{2}$`).MatchString(evidence.RecordedOn) {
		t.Fatal("M13 quality evidence identity is invalid")
	}
	native := evidence.NativeLinux
	if native.Status != "passed" ||
		!regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(native.QualifiedRuntimeRevision) ||
		native.Host == "" || native.PodmanVersion != "4.9.3" ||
		native.PostgresVersion != "18.6" || native.SeaweedFSVersion != "4.47" ||
		!native.SignedInstall || !native.OwnerOnlyInputs || !native.AllRolesReady ||
		native.SchemaLedger.Count != 27 || native.SchemaLedger.Minimum != 1 || native.SchemaLedger.Maximum != 27 ||
		!native.RestartPreservedAsset || !native.PreActivationUpgradeFailure ||
		!native.SignedRollbackActivatedPreviousBundle || !native.RollbackPreservedAsset {
		t.Fatal("native Linux release evidence is incomplete")
	}
	accessibility := evidence.Accessibility
	if accessibility.Status != "passed" || accessibility.Target != "WCAG-2.2-AA" ||
		accessibility.ConformanceClaim != "automated-subset-not-full-wcag-conformance" ||
		accessibility.Browser == "" ||
		accessibility.LocalFeedbackBudgetMs <= 0 || accessibility.LocalFeedbackBudgetMs > 100 ||
		accessibility.ColdFixtureShellBudgetMs <= 0 || accessibility.ColdFixtureShellBudgetMs > 2000 {
		t.Fatal("accessibility evidence exceeded its honest automated boundary")
	}
	required := map[string]bool{
		"accessible-names": false, "keyboard-only": false, "visible-focus": false,
		"text-contrast": false, "target-size": false, "200-percent-equivalent-reflow": false,
		"reduced-motion": false, "status-not-color-only": false,
	}
	for _, scenario := range accessibility.Scenarios {
		if _, exists := required[scenario]; exists {
			required[scenario] = true
		}
	}
	for scenario, present := range required {
		if !present {
			t.Errorf("accessibility evidence omitted %s", scenario)
		}
	}
	capacity := evidence.Capacity
	if capacity.Status != "passed" || capacity.Profile != "technical-preview-5000" ||
		capacity.Findings < 5000 || capacity.ReportBytes < 1<<20 ||
		capacity.UploadMs <= 0 || capacity.UploadMs > 1000 ||
		capacity.ProcessingSeconds <= 0 || capacity.ProcessingSeconds > 120 ||
		capacity.WorkPageRows != 100 || capacity.WorkTotal != capacity.Findings ||
		capacity.WorkP95Ms <= 0 || capacity.WorkP95Ms > 500 ||
		capacity.WorkP99Ms < capacity.WorkP95Ms || capacity.WorkP99Ms > 1500 ||
		capacity.AcknowledgedFindingLoss != 0 ||
		capacity.MaximumContainerMemoryMiB <= 0 || capacity.MaximumContainerMemoryMiB > 512 {
		t.Fatal("technical preview capacity evidence exceeded its declared bounds")
	}
	if len(capacity.ContainerMemoryMiB) != 6 {
		t.Fatal("capacity evidence omitted a release container memory measurement")
	}
	if len(evidence.Limitations) < 5 {
		t.Fatal("quality evidence omitted explicit limitations")
	}
}
