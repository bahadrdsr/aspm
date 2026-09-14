package contracts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequirementMapping(t *testing.T) {
	root := readDocument(t, filepath.Join("testdata", "m00-requirements.json"))
	equal(t, root, "apiVersion", apiVersion)
	equal(t, root, "kind", "RequirementTestMap")
	equal(t, root, "milestone", "M00")
	equal(t, root, "scope", "machine-readable-contracts-not-application-behavior")
	functions := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && strings.HasPrefix(function.Name.Name, "Test") {
				functions[function.Name.Name] = true
			}
		}
	}
	requirements := indexed(t, root, "requirements")
	exactIDs(t, requirements,
		"M00-INSTALL-SCHEMA", "M00-INSTALL-DEFAULTS", "M00-WIZARD-APPROVAL", "M00-TARGET-MATRIX",
		"M00-LAUNCH-FAMILIES", "M00-AI-SCHEMA", "M00-AI-POLICY-SUPPORT", "M00-UX-ACTIONS",
		"M00-UX-MOTION-A11Y", "M00-CAPACITY", "M00-FAILURE-CHURN-RETENTION", "M00-SOURCE-FIDELITY",
	)
	referenced := map[string]bool{}
	for id, requirement := range requirements {
		textAt(t, requirement, "acceptance")
		if len(stringsAt(t, requirement, "sources")) == 0 {
			t.Errorf("%s needs traceability to the approved plan/UX/operator scenarios", id)
		}
		tests := stringsAt(t, requirement, "tests")
		if len(tests) == 0 {
			t.Errorf("%s is not mapped to an executable gate", id)
		}
		for _, name := range tests {
			if !functions[name] {
				t.Errorf("%s references nonexistent Go test %q", id, name)
			}
			referenced[name] = true
		}
	}
	for name := range functions {
		if name != "TestRequirementMapping" && !strings.HasPrefix(name, "TestHarness") && !referenced[name] {
			t.Errorf("acceptance test %s has no requirement mapping", name)
		}
	}
	checklist := indexed(t, root, "manualAcceptance")
	exactIDs(t, checklist,
		"M00-DOC-IDENTITY-LICENSE", "M00-DOC-SOURCE-CI-REGISTRY", "M00-DOC-ARCHITECTURE",
		"M00-DOC-DESIGN-PROTOTYPES", "M00-DOC-DEPENDENCY-REVIEW", "M00-DOC-OPERATOR-RESEARCH", "M00-DOC-SUPPORT-REVIEW",
	)
	for id, item := range checklist {
		textAt(t, item, "acceptance")
		textAt(t, item, "owner")
		equal(t, item, "blockingForMilestone", true)
		status := textAt(t, item, "status")
		if !contains([]string{"pending-review", "approved", "blocked"}, status) {
			t.Errorf("%s has an unknown documentary acceptance state %q", id, status)
		}
		evidence := stringsAt(t, item, "evidenceRefs")
		if status == "approved" && len(evidence) == 0 {
			t.Errorf("%s cannot claim documentary approval without review evidence", id)
		}
	}
}
