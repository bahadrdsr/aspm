package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type JiraTarget struct {
	CredentialType string            `json:"credentialType"`
	CloudID        string            `json:"cloudId"`
	APIBase        string            `json:"apiBase"`
	SiteOrigin     string            `json:"siteOrigin"`
	Project        string            `json:"project"`
	IssueType      string            `json:"issueType"`
	FieldMappings  map[string]string `json:"fieldMappings"`
}

func (target *JiraTarget) UnmarshalJSON(data []byte) error {
	type value JiraTarget
	var decoded value
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*target = JiraTarget(decoded)
	return nil
}

var (
	jiraCloudID     = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	jiraProject     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,254}$`)
	jiraIssueType   = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	jiraCustomField = regexp.MustCompile(`^customfield_[0-9]+$`)
)

func validJiraURL(value, path string) bool {
	if _, err := ValidateDeliveryGateway(value); err != nil {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Path == path && u.RawPath == "" &&
		u.Host == strings.ToLower(u.Host) && value == "https://"+u.Host+path
}

func validJiraTarget(target *JiraTarget) bool {
	if target == nil || target.CredentialType != "oauth2-bearer" ||
		!jiraCloudID.MatchString(target.CloudID) || !jiraProject.MatchString(target.Project) ||
		!jiraIssueType.MatchString(target.IssueType) ||
		!validJiraURL(target.APIBase, "/ex/jira/"+target.CloudID) || !validJiraURL(target.SiteOrigin, "") ||
		target.FieldMappings == nil || len(target.FieldMappings) > 16 {
		return false
	}
	for field, source := range target.FieldMappings {
		if len(field) > 128 || !jiraCustomField.MatchString(field) {
			return false
		}
		switch source {
		case "finding.id", "finding.title", "finding.severity", "asset.name", "finding.deepLink":
		default:
			return false
		}
	}
	return true
}

func validConnectionToken(profile, token string) bool {
	if profile == connectors.TeamsWorkflows {
		_, valid := teamsWorkflowOrigin(token)
		return valid
	}
	if profile == connectors.GenericWebhookV1 {
		return validWebhookSecret(token)
	}
	return validIntegrationToken(token) &&
		(profile != connectors.JiraCloudV3 || !strings.ContainsFunc(token, unicode.IsSpace))
}

func deliveryProfile(r *http.Request) (string, error) {
	values, present := r.URL.Query()["profile"]
	if !present {
		return connectors.SlackWorkspaceBot, nil
	}
	if len(values) != 1 || values[0] != connectors.SlackWorkspaceBot &&
		values[0] != connectors.JiraCloudV3 && values[0] != connectors.TeamsWorkflows &&
		values[0] != connectors.GenericWebhookV1 {
		return "", errInvalid
	}
	return values[0], nil
}

func (a *Application) selectedFindingDelivery(r *http.Request, tx pgx.Tx, workspace string, session authenticatedSession, findingID, connectionID string) (FindingDelivery, error) {
	var delivery FindingDelivery
	var workspaceID string
	if err := tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("workspaces")+`
		WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&workspaceID); err != nil {
		return delivery, err
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return delivery, err
	}
	if !canWrite(Workspace{Role: role}) {
		return delivery, errForbidden
	}
	finding, err := scanWork(tx.QueryRow(r.Context(), `SELECT `+workColumns+a.workFrom()+
		` WHERE f.workspace_id=$1 AND f.id=$2 AND `+a.workVisible()+` FOR SHARE OF f,asset`, workspace, findingID),
		a.config.Now())
	if err != nil {
		return delivery, err
	}
	connection, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2 FOR SHARE`,
		workspace, connectionID))
	if err != nil {
		return delivery, err
	}
	if !connection.Enabled {
		return delivery, errConflict
	}
	delivery = FindingDelivery{
		WorkspaceID: workspace, FindingID: findingID, ConnectionID: connection.ID,
		ConnectionRevision: connection.Revision, Profile: connection.Profile, Channel: connection.Channel,
		RequestedBy: session.User.ID, Jira: connection.Jira, Webhook: connection.Webhook,
		Payload: FindingNotification{
			Title: finding.Title, Body: "Severity: " + finding.Severity + "\nAsset: " + finding.AssetName,
			DeepLink: a.config.PublicOrigin + "/#/work?finding=" + url.QueryEscape(finding.ID),
		},
	}
	if delivery.Profile == connectors.JiraCloudV3 {
		if !validJiraTarget(delivery.Jira) {
			return FindingDelivery{}, errConflict
		}
		if utf8.RuneCountInString(finding.Title) > 255 || strings.TrimSpace(finding.Title) == "" {
			return FindingDelivery{}, errInvalid
		}
		delivery.Payload.Fields = make(map[string]string, len(delivery.Jira.FieldMappings))
		for field, source := range delivery.Jira.FieldMappings {
			switch source {
			case "finding.id":
				delivery.Payload.Fields[field] = finding.ID
			case "finding.title":
				delivery.Payload.Fields[field] = finding.Title
			case "finding.severity":
				delivery.Payload.Fields[field] = finding.Severity
			case "asset.name":
				delivery.Payload.Fields[field] = finding.AssetName
			case "finding.deepLink":
				delivery.Payload.Fields[field] = delivery.Payload.DeepLink
			}
		}
	}
	if delivery.Profile == connectors.TeamsWorkflows {
		if !validTeamsMetadata(connection.Teams) || connection.Jira != nil || connection.Channel != "" {
			return FindingDelivery{}, errConflict
		}
		delivery.Destination = &TeamsDestination{connection.Name, *connection.Teams}
		if !teamsPayloadValid(delivery.Payload) {
			return FindingDelivery{}, errInvalid
		}
	}
	if delivery.Profile == connectors.GenericWebhookV1 {
		if !validWebhookTarget(delivery.Webhook) ||
			!webhookOriginAllowed(delivery.Webhook.Origin, a.config.WebhookOrigins) ||
			connection.Jira != nil || connection.Teams != nil || connection.Channel != "" {
			return FindingDelivery{}, errConflict
		}
	}
	return delivery, nil
}

func (a *Application) previewFindingDelivery(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, findingID string) error {
	var input struct {
		ConnectionID string `json:"connectionId"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.ConnectionID) {
		return errInvalid
	}
	if a.config.PublicOrigin == "" {
		return errUnavailable
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	delivery, err := a.selectedFindingDelivery(r, tx, workspace, session, findingID, input.ConnectionID)
	if err != nil {
		return err
	}
	if delivery.Profile != connectors.JiraCloudV3 &&
		delivery.Profile != connectors.TeamsWorkflows &&
		delivery.Profile != connectors.GenericWebhookV1 {
		return errInvalid
	}
	digest, err := deliveryBinding(delivery)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if delivery.Profile == connectors.TeamsWorkflows {
		a.writeTeamsPreview(w, delivery, "sha256:"+hex.EncodeToString(digest))
		return nil
	}
	if delivery.Profile == connectors.GenericWebhookV1 {
		a.writeWebhookPreview(w, delivery, "sha256:"+hex.EncodeToString(digest))
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": struct {
		WorkspaceID        string              `json:"workspaceId"`
		FindingID          string              `json:"findingId"`
		ConnectionID       string              `json:"connectionId"`
		ConnectionRevision int64               `json:"connectionRevision"`
		Profile            string              `json:"profile"`
		RequestedBy        string              `json:"requestedBy"`
		Jira               *JiraTarget         `json:"jira"`
		Payload            FindingNotification `json:"payload"`
		BindingDigest      string              `json:"bindingDigest"`
		NativeValidation   string              `json:"nativeValidation"`
		ReviewRequirements []string            `json:"reviewRequirements"`
	}{
		delivery.WorkspaceID, delivery.FindingID, delivery.ConnectionID, delivery.ConnectionRevision,
		delivery.Profile, delivery.RequestedBy, delivery.Jira, delivery.Payload, "sha256:" + hex.EncodeToString(digest),
		"not-run", []string{"explicit-queue-consent", "native-required-fields", "jira-permission"},
	}})
	return nil
}
