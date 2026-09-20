//go:build integration

package ai_configuration

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/providers"
)

func TestAIConfigurationDefaultKeylessDisabledAndLocalOnly(t *testing.T) {
	h := newHarness(t, false)
	pol := h.json(h.admin, "GET", policyPath, nil, 200).Policy
	check(t, pol.WorkspaceID == h.admin.Workspace && pol.Mode == "disabled" && pol.Revision == "0" && pol.UpdatedAt == nil && pol.UpdatedBy == nil, "unconfigured policy must be explicit disabled metadata, never inherited authority")
	check(t, h.json(h.admin, "GET", profilesPath, nil, 200).Total == 0, "keyless configuration fabricated a profile")
	p := h.create(h.admin, "local", h.httpTripwire.URL+"/v1", "")
	h.credential("ai_profiles", p.ID, "aspm/ai-credential/v1", "")
	r := h.resolver()
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, nil))
	local := h.mode(h.admin, "local-only")
	assertResolved(t, r, h.ctx, h.admin, p, local, nil, "")
	hostKey := secret(t)
	h.remember(hostKey)
	h.json(h.admin, "POST", profilesPath, h.profileInput("openai", h.tlsTripwire.URL+"/v1", hostKey), 503)
	h.json(h.admin, "POST", profilesPath, h.profileInput("local", h.httpTripwire.URL+"/v1", hostKey), 503)
	h.json(h.admin, "GET", "/api/v1/assets", nil, 200)
	h.mode(h.admin, "disabled")
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, nil))
	h.networkNone()
}

func TestAIConfigurationProfilesPersistSparseUpdatesAADAndSecretRedaction(t *testing.T) {
	h := newHarness(t, true)
	key := secret(t)
	p := h.create(h.admin, "openai", h.tlsTripwire.URL+"/v1", key)
	h.credential("ai_profiles", p.ID, "aspm/ai-credential/v1", key)
	list := h.json(h.admin, "GET", profilesPath+"?limit=1", nil, 200)
	check(t, list.Total == 1 && len(list.Items) == 1, "actual stored profile list is missing")
	allowed := map[string]bool{"id": true, "workspaceId": true, "name": true, "family": true, "endpoint": true, "model": true, "deployment": true, "enabled": true, "structuredOutput": true, "credentialConfigured": true, "revision": true, "createdAt": true, "updatedAt": true}
	check(t, len(list.Items[0]) == len(allowed), "profile metadata contains undeclared or secret fields")
	for key := range list.Items[0] {
		check(t, allowed[key], "profile metadata exposed a secret field")
	}
	updated := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"name": "Human profile label", "model": "synthetic-model-two"}, 200).Profile
	check(t, updated.Revision != p.Revision && updated.Endpoint == p.Endpoint && updated.Family == p.Family && updated.CredentialConfigured && updated.StructuredOutput, "sparse profile update clobbered an omitted field")
	same := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"name": updated.Name}, 200).Profile
	check(t, same.Revision == updated.Revision, "no-op public configuration patch advanced revision")
	nextKey := secret(t)
	h.remember(nextKey)
	rotated := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"apiKey": nextKey}, 200).Profile
	check(t, rotated.Revision != updated.Revision && rotated.Model == updated.Model, "explicit key replacement lost revision or sparse fields")
	replacedAgain := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"apiKey": nextKey}, 200).Profile
	check(t, replacedAgain.Revision != rotated.Revision, "explicit secret replacement must not expose an equality/no-change oracle")
	rotated = replacedAgain
	h.credential("ai_profiles", p.ID, "aspm/ai-credential/v1", nextKey)
	h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"apiKey": nil}, 400)
	localKey := secret(t)
	local := h.create(h.admin, "local", h.httpTripwire.URL+"/v1", localKey)
	h.credential("ai_profiles", local.ID, "aspm/ai-credential/v1", localKey)
	cleared := h.json(h.admin, "PATCH", profilesPath+"/"+local.ID, map[string]any{"apiKey": nil}, 200).Profile
	check(t, !cleared.CredentialConfigured && cleared.Revision != local.Revision, "explicit local key removal was not persisted")
	h.credential("ai_profiles", local.ID, "aspm/ai-credential/v1", "")
	page := h.json(h.admin, "GET", profilesPath+"?limit=1", nil, 200)
	check(t, page.Total == 2 && page.NextCursor != nil, "profile metadata continuation missing")
	next := h.json(h.admin, "GET", profilesPath+"?limit=1&cursor="+*page.NextCursor, nil, 200)
	check(t, len(next.Items) == 1 && next.NextCursor == nil && next.Items[0]["id"] != page.Items[0]["id"], "profile pagination duplicated or lost state")
	h.reopen()
	check(t, h.get(h.admin, p.ID).Revision == rotated.Revision, "profile/key metadata did not survive real core reopen")
	h.stateSnapshot()
	h.networkNone()
}

func TestAIConfigurationFourFamiliesAndEndpointReviewWithoutProviderIO(t *testing.T) {
	h := newHarness(t, true)
	r := h.resolver()
	pol := h.mode(h.admin, "local-only")
	for _, family := range []string{"openai", "azure-foundry", "anthropic", "local"} {
		endpoint, key := h.tlsTripwire.URL+"/v1", secret(t)
		if family == "azure-foundry" {
			endpoint = h.tlsTripwire.URL + "/openai/v1"
		}
		if family == "local" {
			endpoint = h.httpTripwire.URL + "/v1"
			key = ""
		}
		p := h.create(h.admin, family, endpoint, key)
		if family == "local" {
			assertResolved(t, r, h.ctx, h.admin, p, pol, nil, key)
		} else {
			assertDenied(t, r, h.ctx, lookupFor(h.admin, p, nil))
		}
	}
	privateLocal := h.create(h.admin, "local", "http://10.23.45.67:8899/v1", "")
	assertResolved(t, r, h.ctx, h.admin, privateLocal, pol, nil, "")
	unreviewed := h.profileInput("local", h.httpTripwire.URL+"/reviewed", "")
	unreviewed["structuredOutput"] = false
	p := h.json(h.admin, "POST", profilesPath, unreviewed, 201).Profile
	_, err := r.Resolve(h.ctx, lookupFor(h.admin, p, nil))
	check(t, errors.Is(err, providers.ErrCapability), "unreviewed structured-output capability resolved as approved")
	hostPolicy := h.mode(h.admin, "approved-hosted")
	h.json(h.admin, "POST", grantsPath, h.grantInput(p, hostPolicy, h.now().Add(time.Hour)), 409)
	fallback := secret(t)
	h.remember(fallback)
	t.Setenv("OPENAI_API_KEY", fallback)
	t.Setenv("ANTHROPIC_API_KEY", fallback)
	for _, row := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown-family", func(v map[string]any) { v["family"] = "not-a-provider" }},
		{"hosted-http", func(v map[string]any) { v["endpoint"] = h.httpTripwire.URL + "/v1" }},
		{"local-public-http", func(v map[string]any) { v["family"] = "local"; v["endpoint"] = "http://8.8.8.8/v1" }},
		{"local-http-hostname", func(v map[string]any) { v["family"] = "local"; v["endpoint"] = "http://localhost:8080/v1" }},
		{"userinfo", func(v map[string]any) { v["endpoint"] = "https://synthetic:unused@127.0.0.1:1/v1" }},
		{"query", func(v map[string]any) { v["endpoint"] = h.tlsTripwire.URL + "/v1?route=other" }},
		{"fragment", func(v map[string]any) { v["endpoint"] = h.tlsTripwire.URL + "/v1#other" }},
		{"decoded-control", func(v map[string]any) { v["endpoint"] = h.tlsTripwire.URL + "/v1%0A" }},
		{"bad-port", func(v map[string]any) { v["endpoint"] = "https://127.0.0.1:70000/v1" }},
		{"oversize", func(v map[string]any) { v["endpoint"] = h.tlsTripwire.URL + "/" + strings.Repeat("x", 16384) }},
		{"empty-model", func(v map[string]any) { v["model"] = "" }},
		{"missing-foundry-deployment", func(v map[string]any) { v["family"] = "azure-foundry"; v["deployment"] = "" }},
		{"non-foundry-deployment", func(v map[string]any) { v["deployment"] = "not-used" }},
		{"missing-hosted-key", func(v map[string]any) { delete(v, "apiKey") }},
		{"nonboolean-enabled", func(v map[string]any) { v["enabled"] = "true" }},
		{"nonboolean-review", func(v map[string]any) { v["structuredOutput"] = "true" }},
	} {
		input := h.profileInput("openai", h.tlsTripwire.URL+"/v1", secret(t))
		row.change(input)
		h.json(h.admin, "POST", profilesPath, input, 400)
	}
	h.networkNone()
}

func TestAIConfigurationPersistedGrantBindingExpiryRevocationAndRevisions(t *testing.T) {
	h := newHarness(t, true)
	r := h.resolver()
	key := secret(t)
	p := h.create(h.admin, "openai", h.tlsTripwire.URL+"/v1", key)
	pol := h.mode(h.admin, "approved-hosted")
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, nil))
	fake := lookupFor(h.admin, p, nil)
	fake.GrantID = "0123456789abcdef0123456789abcdef"
	assertDenied(t, r, h.ctx, fake)
	g := h.authorize(h.admin, p, pol)
	before := h.stateSnapshot()
	assertResolved(t, r, h.ctx, h.admin, p, pol, &g, key)
	check(t, reflect.DeepEqual(before, h.stateSnapshot()), "read-only resolver mutated configuration/grants")
	for _, bad := range []lookupRequest{
		{WorkspaceID: h.admin.Workspace, ActorID: h.admin.ID, ProfileID: p.ID, GrantID: g.ID, Task: "other-task", DataClass: evidenceClass},
		{WorkspaceID: h.admin.Workspace, ActorID: h.admin.ID, ProfileID: p.ID, GrantID: g.ID, Task: validityTask, DataClass: "other-data"},
	} {
		assertDenied(t, r, h.ctx, bad)
	}
	h.clock.Store(g.ExpiresAt.UnixNano())
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, &g))
	current := h.authorize(h.admin, p, pol)
	revoked := h.json(h.admin, "POST", grantsPath+"/"+current.ID+"/revoke", map[string]any{}, 200).Grant
	check(t, revoked.RevokedAt != nil && revoked.RevokedAt.Equal(h.now()) && revoked.RevokedBy != nil && *revoked.RevokedBy == h.admin.ID, "revoke metadata was not server/session derived")
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, &current))
	again := h.json(h.admin, "POST", grantsPath+"/"+current.ID+"/revoke", map[string]any{}, 200).Grant
	check(t, reflect.DeepEqual(revoked, again), "repeated revoke rewrote original revocation evidence")
	for _, field := range []string{"endpoint", "family", "model", "deployment", "apiKey", "structuredOutput"} {
		family := "openai"
		if field == "deployment" {
			family = "azure-foundry"
		}
		credential := secret(t)
		profile := h.create(h.admin, family, h.tlsTripwire.URL+"/v1/"+field, credential)
		approval := h.authorize(h.admin, profile, pol)
		update := map[string]any{}
		switch field {
		case "endpoint":
			update[field] = h.tlsTripwire.URL + "/v1/changed"
		case "family":
			update[field] = "anthropic"
		case "model":
			update[field] = "different-synthetic-model"
		case "deployment":
			update[field] = "different-synthetic-deployment"
		case "apiKey":
			replacement := secret(t)
			h.remember(replacement)
			update[field] = replacement
		case "structuredOutput":
			update[field] = false
		}
		changed := h.json(h.admin, "PATCH", profilesPath+"/"+profile.ID, update, 200).Profile
		check(t, changed.Revision != profile.Revision, "meaningful reviewed profile change kept an old grant revision")
		if field == "structuredOutput" {
			h.json(h.admin, "PATCH", profilesPath+"/"+profile.ID, map[string]any{"structuredOutput": true}, 200)
		}
		assertDenied(t, r, h.ctx, lookupFor(h.admin, changed, &approval))
	}
	oldPolicyGrant := h.authorize(h.admin, p, pol)
	h.mode(h.admin, "disabled")
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, &oldPolicyGrant))
	nextPolicy := h.mode(h.admin, "approved-hosted")
	check(t, nextPolicy.Revision != pol.Revision, "policy change did not advance server revision")
	assertDenied(t, r, h.ctx, lookupFor(h.admin, p, &oldPolicyGrant))
	fresh := h.authorize(h.admin, p, nextPolicy)
	assertResolved(t, r, h.ctx, h.admin, p, nextPolicy, &fresh, key)
	disabled := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"enabled": false}, 200).Profile
	assertDenied(t, r, h.ctx, lookupFor(h.admin, disabled, &fresh))
	reenabled := h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"enabled": true}, 200).Profile
	assertDenied(t, r, h.ctx, lookupFor(h.admin, reenabled, &fresh))
	h.networkNone()
}

func TestAIConfigurationMetadataRBACForeignScopeAndForgedAuthority(t *testing.T) {
	h := newHarness(t, true)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	key := secret(t)
	p := h.create(h.admin, "openai", h.tlsTripwire.URL+"/v1", key)
	pol := h.mode(h.admin, "approved-hosted")
	g := h.authorize(h.admin, p, pol)
	r := h.resolver()
	for _, who := range []actor{writer, viewer} {
		h.json(who, "GET", profilesPath, nil, 200)
		h.json(who, "GET", profilesPath+"/"+p.ID, nil, 200)
		h.json(who, "GET", policyPath, nil, 200)
		h.json(who, "GET", grantsPath, nil, 200)
		h.json(who, "GET", grantsPath+"/"+g.ID, nil, 200)
		h.json(who, "POST", profilesPath, h.profileInput("openai", h.tlsTripwire.URL+"/v1", key), 403)
		h.json(who, "PATCH", profilesPath+"/"+p.ID, map[string]any{"enabled": false}, 403)
		h.json(who, "PATCH", policyPath, map[string]string{"mode": "disabled"}, 403)
		h.json(who, "POST", grantsPath, h.grantInput(p, pol, h.now().Add(time.Hour)), 403)
		h.json(who, "POST", grantsPath+"/"+g.ID+"/revoke", map[string]any{}, 403)
	}
	h.json(actor{}, "GET", profilesPath, nil, 401)
	assertResolved(t, r, h.ctx, writer, p, pol, &g, key)
	assertDenied(t, r, h.ctx, lookupFor(viewer, p, &g))
	other := h.otherWorkspace()
	h.json(other, "GET", profilesPath+"/"+p.ID, nil, 404)
	h.json(other, "PATCH", profilesPath+"/"+p.ID, map[string]string{"name": "wrong scope"}, 404)
	h.json(other, "GET", grantsPath+"/"+g.ID, nil, 404)
	h.json(other, "POST", grantsPath+"/"+g.ID+"/revoke", map[string]any{}, 404)
	check(t, h.json(other, "GET", profilesPath, nil, 200).Total == 0 && h.json(other, "GET", grantsPath, nil, 200).Total == 0, "AI metadata crossed workspace scope")
	assertDenied(t, r, h.ctx, lookupFor(other, p, &g))
	for _, field := range []string{"workspaceId", "id", "createdBy", "headers", "approvalRef", "policy"} {
		input := h.profileInput("openai", h.tlsTripwire.URL+"/v1", key)
		input[field] = "untrusted"
		h.json(h.admin, "POST", profilesPath, input, 400)
	}
	h.json(h.admin, "PATCH", policyPath, map[string]any{"mode": "approved-hosted", "allowedDestinations": []string{h.tlsTripwire.URL}}, 400)
	for _, field := range []string{"workspaceId", "id", "grantedBy", "createdAt", "revokedAt", "approvalRef"} {
		input := h.grantInput(p, pol, h.now().Add(time.Hour))
		input[field] = "untrusted"
		h.json(h.admin, "POST", grantsPath, input, 400)
	}
	for _, field := range []string{"destination", "profileRevision", "policyRevision"} {
		input := h.grantInput(p, pol, h.now().Add(time.Hour))
		input[field] = "stale-revision"
		if field == "destination" {
			input[field] = h.tlsTripwire.URL + "/not-the-reviewed-destination"
		}
		h.json(h.admin, "POST", grantsPath, input, 409)
	}
	exactDestination := h.grantInput(p, pol, h.now().Add(time.Hour))
	exactDestination["destination"] = p.Endpoint + "/"
	h.json(h.admin, "POST", grantsPath, exactDestination, 409)
	for _, field := range []string{"task", "dataClass", "expiresAt"} {
		input := h.grantInput(p, pol, h.now().Add(time.Hour))
		input[field] = "invalid"
		h.json(h.admin, "POST", grantsPath, input, 400)
	}
	h.json(h.admin, "POST", grantsPath, h.grantInput(p, pol, h.now()), 400)
	h.json(h.admin, "POST", grantsPath+"/"+g.ID+"/revoke", map[string]any{"revokedBy": writer.ID}, 400)
	h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, map[string]string{"role": "viewer"}, 200)
	assertDenied(t, r, h.ctx, lookupFor(writer, p, &g))
	h.networkNone()
}

func TestAIConfigurationPersistenceConcurrentUpdatesAndPublishedV6Upgrade(t *testing.T) {
	t.Run("fresh-concurrent-and-credential-compatibility", func(t *testing.T) {
		h := newHarness(t, true)
		key := secret(t)
		p := h.create(h.admin, "openai", h.tlsTripwire.URL+"/v1", key)
		pol := h.mode(h.admin, "approved-hosted")
		old := h.authorize(h.admin, p, pol)
		requests := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
		defer cancel()
		one := h.makeRequest(ctx, h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]string{"name": "Concurrent name"})
		two := h.makeRequest(ctx, h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]string{"model": "concurrent-synthetic-model"})
		done := make(chan struct{}, 2)
		go func() { h.core.Handler.ServeHTTP(requests[0], one); done <- struct{}{} }()
		go func() { h.core.Handler.ServeHTTP(requests[1], two); done <- struct{}{} }()
		for range requests {
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("owned concurrent sparse configuration did not finish")
			}
		}
		for _, response := range requests {
			check(t, response.Code == 200, "concurrent valid sparse patch failed")
			h.noSecrets(response.Body.Bytes())
		}
		updated := h.get(h.admin, p.ID)
		check(t, updated.Name == "Concurrent name" && updated.Model == "concurrent-synthetic-model" && updated.Endpoint == p.Endpoint && updated.Revision != p.Revision, "concurrent configuration clobbered unrelated confirmed fields")
		r := h.resolver()
		assertDenied(t, r, h.ctx, lookupFor(h.admin, updated, &old))
		slackKey, sourceKey := secret(t), secret(t)
		h.remember(slackKey)
		h.remember(sourceKey)
		slack := h.json(h.admin, "POST", "/api/v1/integrations/connections", map[string]any{"profile": "slack-workspace-bot", "name": "Unchanged Slack credential", "channel": "C123", "token": slackKey, "enabled": true}, 201)
		source := h.json(h.admin, "POST", "/api/v1/sources", map[string]any{"profile": "github-cloud-app", "name": "Unchanged source credential", "repository": "owned/repository", "token": sourceKey, "enabled": true}, 201)
		h.credential("integration_connections", slack.Connection.ID, "aspm/slack-credential/v1", slackKey)
		h.credential("source_connections", source.Source.ID, "aspm/source-credential/v1\x00github-cloud-app", sourceKey)
		current := h.authorize(h.admin, updated, pol)
		h.reopen()
		must(t, "close lookup before independent reopen", r.Close())
		fresh := h.resolver()
		assertResolved(t, fresh, h.ctx, h.admin, h.get(h.admin, p.ID), h.json(h.admin, "GET", policyPath, nil, 200).Policy, &current, key)
		for _, table := range []string{"findings", "observations", "imports", "coverage", "finding_deliveries", "source_collections"} {
			var count int
			must(t, "inspect unchanged execution-domain counts", h.db.QueryRow(h.ctx, "SELECT count(*) FROM "+h.table(table)).Scan(&count))
			check(t, count == 0, "AI configuration executed or created unrelated analysis/source/finding work")
		}
		h.stateSnapshot()
		h.networkNone()
	})
	t.Run("published-v6-upgrade", func(t *testing.T) { testPublishedUpgrade(t) })
}
