//go:build integration && assessment_runtime_probe

package assessment_runtime

import (
	"testing"

	"github.com/bahadrdsr/aspm/internal/service"
)

func TestAssessmentRuntimeExistingEnvironmentRoleProbe(t *testing.T) {
	f := newFixture(t)
	baseEnvironment(t, f.database)
	unset(t, "ASPM_DB_MAX_CONNECTIONS")
	t.Setenv("ASPM_LISTEN", ownedAddress(t))
	t.Setenv("ASPM_ASSESSMENT_SCOPE", f.scope)
	unset(t, "ASPM_INTEGRATION_ENCRYPTION_KEY")
	config, err := service.Environment("assessment")
	if err != nil {
		t.Logf("Actual existing service.Environment(\"assessment\") error type=%T; no service/command acceptance inferred", err)
		t.Fatal("actual assessment role Environment rejected valid explicit keyless assessment settings")
	}
	check(t, config.Jobs.MaxConnections == 1, "actual assessment role did not retain independent one-connection default")
	t.Log("Actual role Environment returned; this prerequisite probe is not typed field forwarding or command acceptance.")
}
