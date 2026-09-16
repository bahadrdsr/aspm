package app

import "net/http"

type IntegrationSummary struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	SupportMaturity  string   `json:"supportMaturity"`
	ConnectionState  string   `json:"connectionState"`
	ReadyToConnect   bool     `json:"readyToConnect"`
	Capabilities     []string `json:"capabilities"`
	LiveVerification struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"liveVerification"`
}

func (a *Application) catalog(w http.ResponseWriter) error {
	items := make([]IntegrationSummary, 0, 8)
	for _, family := range [][2]string{
		{"github", "GitHub"}, {"gitlab", "GitLab"}, {"azure-devops", "Azure DevOps"},
		{"aws", "AWS"}, {"azure", "Azure"}, {"jira", "Jira"}, {"teams", "Microsoft Teams"}, {"slack", "Slack"},
	} {
		entry := IntegrationSummary{
			ID: family[0], Name: family[1], Kind: "native", SupportMaturity: "planned",
			ConnectionState: "unconfigured", ReadyToConnect: false, Capabilities: []string{},
		}
		entry.LiveVerification.State = "not-run"
		entry.LiveVerification.Reason = "Native connector implementation has not landed; report imports do not establish native support."
		items = append(items, entry)
	}
	writeJSON(w, 200, map[string]any{"dataOrigin": "live", "items": items})
	return nil
}
