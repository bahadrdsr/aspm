//go:build integration && ai_configuration_fixture

package ai_configuration

import "testing"

func TestAIConfigurationOwnedPGAndNetworkNoneFixture(t *testing.T) {
	h := newHarness(t, true)
	h.json(h.admin, "GET", "/api/v1/assets", nil, 200)
	h.networkNone()
	t.Log("owned PG session/bootstrap fixture and HTTP-none guard PASS; no AI configuration, inference or resolver acceptance claimed")
}
