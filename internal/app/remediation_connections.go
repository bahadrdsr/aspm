package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type IntegrationConnection struct {
	ID                   string         `json:"id"`
	WorkspaceID          string         `json:"workspaceId"`
	Profile              string         `json:"profile"`
	Name                 string         `json:"name"`
	Channel              string         `json:"channel"`
	Enabled              bool           `json:"enabled"`
	CredentialConfigured bool           `json:"credentialConfigured"`
	Revision             int64          `json:"revision"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
	Jira                 *JiraTarget    `json:"jira,omitempty"`
	Teams                *TeamsMetadata `json:"teams,omitempty"`
	Webhook              *WebhookTarget `json:"webhook,omitempty"`
	PermissionState      string         `json:"permissionState,omitempty"`
}

type integrationConnectionRecord struct {
	IntegrationConnection
	ciphertext []byte
}

func (connection IntegrationConnection) MarshalJSON() ([]byte, error) {
	if connection.Profile != connectors.GenericWebhookV1 {
		type value IntegrationConnection
		return json.Marshal(value(connection))
	}
	return json.Marshal(struct {
		ID                   string         `json:"id"`
		WorkspaceID          string         `json:"workspaceId"`
		Profile              string         `json:"profile"`
		Name                 string         `json:"name"`
		Enabled              bool           `json:"enabled"`
		CredentialConfigured bool           `json:"credentialConfigured"`
		Revision             int64          `json:"revision"`
		CreatedAt            time.Time      `json:"createdAt"`
		UpdatedAt            time.Time      `json:"updatedAt"`
		Webhook              *WebhookTarget `json:"webhook"`
		PermissionState      string         `json:"permissionState"`
	}{
		connection.ID, connection.WorkspaceID, connection.Profile, connection.Name,
		connection.Enabled, connection.CredentialConfigured, connection.Revision,
		connection.CreatedAt, connection.UpdatedAt, connection.Webhook,
		connection.PermissionState,
	})
}

const integrationConnectionColumns = `id,workspace_id,profile,name,channel,enabled,
	revision,created_at,updated_at,credential_ciphertext,jira_target,teams_target,webhook_target`

func scanIntegrationConnection(row pgx.Row) (integrationConnectionRecord, error) {
	var record integrationConnectionRecord
	var target, teams, webhook []byte
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.Profile, &record.Name, &record.Channel,
		&record.Enabled, &record.Revision, &record.CreatedAt, &record.UpdatedAt, &record.ciphertext,
		&target, &teams, &webhook)
	if err == nil && len(target) != 0 {
		err = json.Unmarshal(target, &record.Jira)
	}
	if err == nil && len(teams) != 0 {
		err = json.Unmarshal(teams, &record.Teams)
	}
	if err == nil && len(webhook) != 0 {
		err = json.Unmarshal(webhook, &record.Webhook)
	}
	if record.Profile == connectors.JiraCloudV3 || record.Profile == connectors.TeamsWorkflows ||
		record.Profile == connectors.GenericWebhookV1 {
		record.PermissionState = "not-verified"
	}
	record.CredentialConfigured = len(record.ciphertext) > 0
	return record, err
}

func (a *Application) createIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input struct {
		Profile     string                       `json:"profile"`
		Name        string                       `json:"name"`
		Channel     optional[string]             `json:"channel"`
		Token       optional[string]             `json:"token"`
		Enabled     *bool                        `json:"enabled"`
		Jira        optional[JiraTarget]         `json:"jira"`
		WorkflowURL optional[string]             `json:"workflowUrl"`
		Teams       optional[teamsConfiguration] `json:"teams"`
		WebhookURL  optional[string]             `json:"webhookUrl"`
		Secret      optional[string]             `json:"secret"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if !validText(input.Name, 256) || input.Enabled == nil {
		return errInvalid
	}
	channel, credential := "", ""
	var teams *TeamsMetadata
	var webhook *WebhookTarget
	switch input.Profile {
	case connectors.SlackWorkspaceBot:
		if input.Channel.Value == nil || !integrationChannel.MatchString(*input.Channel.Value) || input.Jira.Set ||
			input.WorkflowURL.Set || input.Teams.Set || input.WebhookURL.Set || input.Secret.Set {
			return errInvalid
		}
		channel = *input.Channel.Value
	case connectors.JiraCloudV3:
		if input.Channel.Set || !validJiraTarget(input.Jira.Value) || input.WorkflowURL.Set || input.Teams.Set ||
			input.WebhookURL.Set || input.Secret.Set {
			return errInvalid
		}
	case connectors.TeamsWorkflows:
		if input.Channel.Set || input.Token.Set || input.Jira.Set || input.WorkflowURL.Value == nil ||
			!validTeamsConfiguration(input.Teams.Value) || input.WebhookURL.Set || input.Secret.Set {
			return errInvalid
		}
		origin, valid := teamsWorkflowOrigin(*input.WorkflowURL.Value)
		if !valid {
			return errInvalid
		}
		credential = *input.WorkflowURL.Value
		teams = &TeamsMetadata{origin, input.Teams.Value.ChannelType, input.Teams.Value.OwnershipAcknowledged}
	case connectors.GenericWebhookV1:
		if input.Channel.Set || input.Token.Set || input.Jira.Set || input.WorkflowURL.Set || input.Teams.Set ||
			input.WebhookURL.Value == nil || input.Secret.Value == nil ||
			!validWebhookSecret(*input.Secret.Value) {
			return errInvalid
		}
		var valid bool
		webhook, valid = parseWebhookTarget(*input.WebhookURL.Value, a.config.WebhookOrigins)
		if !valid {
			return errInvalid
		}
		credential = *input.Secret.Value
	default:
		return errInvalid
	}
	if input.Profile == connectors.SlackWorkspaceBot || input.Profile == connectors.JiraCloudV3 {
		if input.Token.Value == nil || !validConnectionToken(input.Profile, *input.Token.Value) {
			return errInvalid
		}
		credential = *input.Token.Value
	}
	if a.integrationCredentials == nil {
		return errUnavailable
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var workspaceID string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("workspaces")+`
		WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&workspaceID); err != nil {
		return err
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	id := newID()
	ciphertext, err := sealConnectionToken(a.integrationCredentials, input.Profile, workspace, id, credential)
	if err != nil {
		return err
	}
	var target, teamTarget, webhookTarget []byte
	if input.Jira.Value != nil {
		target, err = json.Marshal(input.Jira.Value)
		if err != nil {
			return err
		}
	}
	if teams != nil {
		teamTarget, err = json.Marshal(teams)
		if err != nil {
			return err
		}
	}
	if webhook != nil {
		webhookTarget, err = json.Marshal(webhook)
		if err != nil {
			return err
		}
	}
	record, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("integration_connections")+`
		(id,workspace_id,profile,name,channel,credential_ciphertext,enabled,created_at,updated_at,
		 jira_target,teams_target,webhook_target)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10,$11) RETURNING `+integrationConnectionColumns,
		id, workspace, input.Profile, input.Name, channel, ciphertext, *input.Enabled,
		a.config.Now().UTC(), target, teamTarget, webhookTarget))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) updateIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		Name        optional[string]             `json:"name"`
		Channel     optional[string]             `json:"channel"`
		Token       optional[string]             `json:"token"`
		Enabled     optional[bool]               `json:"enabled"`
		Jira        optional[JiraTarget]         `json:"jira"`
		WorkflowURL optional[string]             `json:"workflowUrl"`
		Teams       optional[teamsConfiguration] `json:"teams"`
		WebhookURL  optional[string]             `json:"webhookUrl"`
		Secret      optional[string]             `json:"secret"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if input.Name.Set && (input.Name.Value == nil || !validText(*input.Name.Value, 256)) ||
		input.Channel.Set && (input.Channel.Value == nil || !integrationChannel.MatchString(*input.Channel.Value)) ||
		input.Token.Set && (input.Token.Value == nil || !validIntegrationToken(*input.Token.Value)) ||
		input.Enabled.Set && input.Enabled.Value == nil ||
		input.Jira.Set && !validJiraTarget(input.Jira.Value) ||
		input.WorkflowURL.Set && (input.WorkflowURL.Value == nil || !validConnectionToken(connectors.TeamsWorkflows, *input.WorkflowURL.Value)) ||
		input.Teams.Set && !validTeamsConfiguration(input.Teams.Value) ||
		input.WebhookURL.Set && input.WebhookURL.Value == nil ||
		input.Secret.Set && (input.Secret.Value == nil || !validWebhookSecret(*input.Secret.Value)) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	record, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	if record.Profile == connectors.JiraCloudV3 && input.Channel.Set ||
		record.Profile == connectors.SlackWorkspaceBot && input.Jira.Set ||
		record.Profile != connectors.TeamsWorkflows && (input.WorkflowURL.Set || input.Teams.Set) ||
		record.Profile == connectors.TeamsWorkflows && (input.Token.Set || input.Channel.Set || input.Jira.Set) ||
		record.Profile != connectors.GenericWebhookV1 && (input.WebhookURL.Set || input.Secret.Set) ||
		record.Profile == connectors.GenericWebhookV1 &&
			(input.Token.Set || input.Channel.Set || input.Jira.Set || input.WorkflowURL.Set || input.Teams.Set) ||
		input.Token.Set && !validConnectionToken(record.Profile, *input.Token.Value) {
		return errInvalid
	}
	changed := false
	if input.Name.Set && record.Name != *input.Name.Value {
		record.Name, changed = *input.Name.Value, true
	}
	if input.Channel.Set && record.Channel != *input.Channel.Value {
		record.Channel, changed = *input.Channel.Value, true
	}
	if input.Enabled.Set && record.Enabled != *input.Enabled.Value {
		record.Enabled, changed = *input.Enabled.Value, true
	}
	if input.Jira.Set && !reflect.DeepEqual(record.Jira, input.Jira.Value) {
		record.Jira, changed = input.Jira.Value, true
	}
	if input.Teams.Set {
		if !validTeamsMetadata(record.Teams) {
			return errUnavailable
		}
		next := &TeamsMetadata{record.Teams.WorkflowOrigin, input.Teams.Value.ChannelType, input.Teams.Value.OwnershipAcknowledged}
		if !reflect.DeepEqual(record.Teams, next) {
			record.Teams, changed = next, true
		}
	}
	if input.WebhookURL.Set {
		next, valid := parseWebhookTarget(*input.WebhookURL.Value, a.config.WebhookOrigins)
		if !valid {
			return errInvalid
		}
		if !reflect.DeepEqual(record.Webhook, next) {
			record.Webhook, changed = next, true
		}
	}
	credential := input.Token
	if input.WorkflowURL.Set {
		credential = input.WorkflowURL
		origin, _ := teamsWorkflowOrigin(*input.WorkflowURL.Value)
		record.Teams = &TeamsMetadata{origin, "standard", true}
	}
	if input.Secret.Set {
		credential = input.Secret
	}
	if credential.Set {
		prior, err := openConnectionToken(a.integrationCredentials, record.Profile, workspace, id, record.ciphertext)
		if err != nil {
			return err
		}
		same := bytes.Equal(prior, []byte(*credential.Value))
		clear(prior)
		if !same {
			record.ciphertext, err = sealConnectionToken(a.integrationCredentials, record.Profile, workspace, id, *credential.Value)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		var target, teamTarget, webhookTarget []byte
		if record.Jira != nil {
			target, err = json.Marshal(record.Jira)
			if err != nil {
				return err
			}
		}
		if record.Teams != nil {
			teamTarget, err = json.Marshal(record.Teams)
			if err != nil {
				return err
			}
		}
		if record.Webhook != nil {
			webhookTarget, err = json.Marshal(record.Webhook)
			if err != nil {
				return err
			}
		}
		record, err = scanIntegrationConnection(tx.QueryRow(r.Context(), `UPDATE `+a.table("integration_connections")+`
			SET name=$3,channel=$4,credential_ciphertext=$5,enabled=$6,revision=revision+1,
				updated_at=$7,jira_target=$8,teams_target=$9,webhook_target=$10
			WHERE workspace_id=$1 AND id=$2 RETURNING `+integrationConnectionColumns,
			workspace, id, record.Name, record.Channel, record.ciphertext, record.Enabled,
			a.config.Now().UTC(), target, teamTarget, webhookTarget))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) getIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanIntegrationConnection(a.pool.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) listIntegrationConnections(w http.ResponseWriter, r *http.Request, workspace string) error {
	profile, err := deliveryProfile(r)
	if err != nil {
		return err
	}
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("integration_connections")+`
		WHERE workspace_id=$1 AND profile=$2`, workspace, profile).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+integrationConnectionColumns+` FROM `+a.table("integration_connections")+`
		WHERE workspace_id=$1 AND profile=$2 AND id>$3 ORDER BY id LIMIT $4`, workspace, profile, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []IntegrationConnection{}
	for rows.Next() {
		record, scanErr := scanIntegrationConnection(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.IntegrationConnection)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}
