package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func load(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(Inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestUpgradeRoutes: the feature-023 routes, their permissions, body limits
// and schemas (contracts/inventory-http.md).
func TestUpgradeRoutes(t *testing.T) {
	doc := load(t)
	routes := []struct {
		method, path, op, perm string
		body                   float64
	}{
		{"GET", "/api/inventory/v1/agents", "listAgents", "agents:manage", 0},
		{"GET", "/api/inventory/v1/agents/{id}", "getAgent", "agents:manage", 0},
		{"GET", "/api/inventory/v1/agents/upgrades", "listAgentUpgrades", "agents:manage", 0},
		{"POST", "/api/inventory/v1/agents/upgrades", "requestAgentUpgrades", "agents:manage", 65536},
		{"POST", "/api/inventory/v1/agents/upgrades/{id}/cancel", "cancelAgentUpgrade", "agents:manage", 1024},
		{"GET", "/api/inventory/v1/agent-releases", "listAgentReleases", "agents:manage", 0},
		{"GET", "/api/inventory/v1/agents/upgrade-policy", "getAgentUpgradePolicy", "agents:manage", 0},
		{"PUT", "/api/inventory/v1/agents/upgrade-policy", "updateAgentUpgradePolicy", "agentupgrades:manage", 4096},
		{"POST", "/api/inventory/v1/agents/upgrade-policy/resume", "resumeAgentUpgradePolicy", "agentupgrades:manage", 1024},
	}
	for _, r := range routes {
		item := doc.Paths.Find(r.path)
		if item == nil {
			t.Fatalf("%s %s not declared", r.method, r.path)
		}
		op := item.GetOperation(r.method)
		if op == nil || op.OperationID != r.op {
			t.Fatalf("%s %s operation = %v", r.method, r.path, op)
		}
		if perm, _ := op.Extensions["x-freya-permission"].(string); perm != r.perm {
			t.Errorf("%s %s permission = %q, want %q", r.method, r.path, perm, r.perm)
		}
		if limit, _ := op.Extensions["x-freya-max-body-bytes"].(float64); limit != r.body {
			t.Errorf("%s %s body limit = %v, want %v", r.method, r.path, limit, r.body)
		}
		if r.method == "POST" {
			csrf := false
			for _, p := range op.Parameters {
				csrf = csrf || p.Value.Name == "X-CSRF-Token"
			}
			if !csrf {
				t.Errorf("%s %s lacks the CSRF header", r.method, r.path)
			}
		}
	}
	for _, name := range []string{"AgentFleetEntry", "AgentFleet", "AgentUpgrade", "AgentDetail", "UpgradeRequest", "UpgradeBatchResult",
		"AgentUpgradeList", "AgentReleaseInfo", "AgentReleaseList", "UpgradePolicy"} {
		if doc.Components.Schemas[name] == nil {
			t.Errorf("schema %s missing", name)
		}
	}
	req := doc.Components.Schemas["UpgradeRequest"].Value
	if req.AdditionalProperties.Has == nil || *req.AdditionalProperties.Has || len(req.OneOf) != 2 || req.Properties["agent_ids"].Value.MaxItems == nil ||
		*req.Properties["agent_ids"].Value.MaxItems != 1000 {
		t.Fatalf("UpgradeRequest = %+v", req)
	}
	pol := doc.Components.Schemas["UpgradePolicy"].Value
	if pol.AdditionalProperties.Has == nil || *pol.AdditionalProperties.Has || len(pol.Required) != 6 || *pol.Properties["max_concurrent"].Value.Max != 100 ||
		pol.Properties["window_start"].Value.Pattern == "" || !pol.Properties["paused"].Value.ReadOnly {
		t.Fatalf("UpgradePolicy = %+v", pol)
	}
	entry := doc.Components.Schemas["AgentFleetEntry"].Value
	for _, k := range []string{"agent_id", "host_id", "version", "connected_at", "upgrade_state", "target_version"} {
		if entry.Properties[k] == nil {
			t.Errorf("AgentFleetEntry lacks %s", k)
		}
	}
}
