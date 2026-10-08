package app

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

const maxNotificationPolicies = 32

var notificationChangeKinds = []string{"new", "changed", "reopened"}

type NotificationPolicyConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Profile  string `json:"profile"`
	Revision int64  `json:"revision"`
	Enabled  bool   `json:"enabled"`
	Current  bool   `json:"current"`
}

type NotificationPolicy struct {
	ID                 string                       `json:"id"`
	WorkspaceID        string                       `json:"workspaceId"`
	Name               string                       `json:"name"`
	ConnectionID       string                       `json:"connectionId"`
	ConnectionProfile  string                       `json:"connectionProfile"`
	ConnectionRevision int64                        `json:"connectionRevision"`
	Enabled            bool                         `json:"enabled"`
	ChangeKinds        []string                     `json:"changeKinds"`
	MinimumSeverity    string                       `json:"minimumSeverity"`
	Revision           int64                        `json:"revision"`
	Epoch              int64                        `json:"epoch"`
	ApprovedBy         string                       `json:"approvedBy"`
	ApprovedByName     string                       `json:"approvedByName"`
	Rationale          string                       `json:"rationale"`
	CreatedAt          time.Time                    `json:"createdAt"`
	UpdatedAt          time.Time                    `json:"updatedAt"`
	Connection         NotificationPolicyConnection `json:"connection"`
}

type NotificationPolicyEvent struct {
	ID                    string    `json:"id"`
	WorkspaceID           string    `json:"workspaceId"`
	PolicyID              string    `json:"policyId"`
	PolicyRevision        int64     `json:"policyRevision"`
	FindingID             string    `json:"findingId"`
	FindingChangeRevision int64     `json:"findingChangeRevision"`
	Outcome               string    `json:"outcome"`
	DeliveryID            *string   `json:"deliveryId"`
	CreatedAt             time.Time `json:"createdAt"`
}

type notificationPolicyRevision struct {
	ID, WorkspaceID, PolicyID, Name, ConnectionID, ConnectionProfile string
	MinimumSeverity, ActorID, ActorName, Rationale                   string
	ConnectionRevision, Revision, Epoch                              int64
	Enabled                                                          bool
	ChangeKinds                                                      []string
	CreatedAt                                                        time.Time
}

const notificationPolicyColumns = `p.id,p.workspace_id,p.name,p.connection_id,p.connection_profile,
	p.connection_revision,p.enabled,p.change_kinds,p.minimum_severity,p.revision,p.epoch,p.approved_by,
	r.actor_name,p.rationale,p.created_at,p.updated_at,
	c.id,c.name,c.profile,c.revision,c.enabled`

func scanNotificationPolicy(row pgx.Row) (NotificationPolicy, error) {
	var policy NotificationPolicy
	err := row.Scan(&policy.ID, &policy.WorkspaceID, &policy.Name, &policy.ConnectionID,
		&policy.ConnectionProfile, &policy.ConnectionRevision, &policy.Enabled, &policy.ChangeKinds,
		&policy.MinimumSeverity, &policy.Revision, &policy.Epoch, &policy.ApprovedBy,
		&policy.ApprovedByName, &policy.Rationale, &policy.CreatedAt, &policy.UpdatedAt,
		&policy.Connection.ID, &policy.Connection.Name, &policy.Connection.Profile,
		&policy.Connection.Revision, &policy.Connection.Enabled)
	if err == nil {
		policy.CreatedAt = policy.CreatedAt.UTC()
		policy.UpdatedAt = policy.UpdatedAt.UTC()
		policy.Connection.Current = policy.Connection.Profile == policy.ConnectionProfile &&
			policy.Connection.Revision == policy.ConnectionRevision
	}
	return policy, err
}

func notificationPolicyFrom() string {
	return ` FROM {{notification_policies}} p
		JOIN {{notification_policy_revisions}} r
		ON r.workspace_id=p.workspace_id AND r.policy_id=p.id AND r.revision=p.revision
		JOIN {{integration_connections}} c
		ON c.workspace_id=p.workspace_id AND c.id=p.connection_id`
}

func (a *Application) notificationPolicyFrom() string {
	value := notificationPolicyFrom()
	value = strings.ReplaceAll(value, "{{notification_policies}}", a.table("notification_policies"))
	value = strings.ReplaceAll(value, "{{notification_policy_revisions}}", a.table("notification_policy_revisions"))
	return strings.ReplaceAll(value, "{{integration_connections}}", a.table("integration_connections"))
}

func validNotificationConnectionProfile(profile string) bool {
	return profile == connectors.SlackWorkspaceBot ||
		profile == connectors.TeamsWorkflows ||
		profile == connectors.JiraCloudV3
}

func validNotificationSeverity(value string) bool {
	switch value {
	case "critical", "high", "medium", "low", "info":
		return true
	default:
		return false
	}
}

func normalizeNotificationChangeKinds(values []string) ([]string, bool) {
	if len(values) == 0 || len(values) > len(notificationChangeKinds) {
		return nil, false
	}
	selected := make(map[string]bool, len(values))
	for _, value := range values {
		if selected[value] || !slices.Contains(notificationChangeKinds, value) {
			return nil, false
		}
		selected[value] = true
	}
	result := make([]string, 0, len(values))
	for _, value := range notificationChangeKinds {
		if selected[value] {
			result = append(result, value)
		}
	}
	return result, true
}

func (a *Application) lockNotificationPolicyEpoch(ctx context.Context, tx pgx.Tx, workspace string) (int64, error) {
	var epoch int64
	err := tx.QueryRow(ctx, `UPDATE `+a.table("workspaces")+`
		SET notification_policy_epoch=notification_policy_epoch+1
		WHERE id=$1 RETURNING notification_policy_epoch`, workspace).Scan(&epoch)
	return epoch, err
}

func (a *Application) notificationPolicyConnection(ctx context.Context, tx pgx.Tx,
	workspace, id string) (integrationConnectionRecord, error) {
	record, err := scanIntegrationConnection(tx.QueryRow(ctx, `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+`
		WHERE workspace_id=$1 AND id=$2 FOR SHARE`, workspace, id))
	if err != nil {
		return record, err
	}
	if !validNotificationConnectionProfile(record.Profile) {
		return record, errInvalid
	}
	return record, nil
}

func (a *Application) insertNotificationPolicyRevision(ctx context.Context, tx pgx.Tx,
	policy NotificationPolicy, actorName string) error {
	_, err := tx.Exec(ctx, `INSERT INTO `+a.table("notification_policy_revisions")+`
		(id,workspace_id,policy_id,name,connection_id,connection_profile,connection_revision,
		 enabled,change_kinds,minimum_severity,revision,epoch,actor_id,actor_name,rationale,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		newID(), policy.WorkspaceID, policy.ID, policy.Name, policy.ConnectionID,
		policy.ConnectionProfile, policy.ConnectionRevision, policy.Enabled, policy.ChangeKinds,
		policy.MinimumSeverity, policy.Revision, policy.Epoch, policy.ApprovedBy,
		actorName, policy.Rationale, policy.UpdatedAt)
	return err
}

func (a *Application) createNotificationPolicy(w http.ResponseWriter, r *http.Request,
	workspace string, session authenticatedSession) error {
	var input struct {
		Name            string   `json:"name"`
		ConnectionID    string   `json:"connectionId"`
		Enabled         *bool    `json:"enabled"`
		ChangeKinds     []string `json:"changeKinds"`
		MinimumSeverity string   `json:"minimumSeverity"`
		Rationale       string   `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	changeKinds, valid := normalizeNotificationChangeKinds(input.ChangeKinds)
	if !validText(input.Name, 256) || !validID(input.ConnectionID) || input.Enabled == nil ||
		!valid || !validNotificationSeverity(input.MinimumSeverity) || !validText(input.Rationale, 8192) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	epoch, err := a.lockNotificationPolicyEpoch(r.Context(), tx, workspace)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("notification_policies")+`
		WHERE workspace_id=$1`, workspace).Scan(&count); err != nil {
		return err
	}
	if count >= maxNotificationPolicies {
		return errConflict
	}
	connection, err := a.notificationPolicyConnection(r.Context(), tx, workspace, input.ConnectionID)
	if err != nil {
		return err
	}
	now := a.config.Now().UTC()
	policy := NotificationPolicy{
		ID: newID(), WorkspaceID: workspace, Name: input.Name, ConnectionID: connection.ID,
		ConnectionProfile: connection.Profile, ConnectionRevision: connection.Revision,
		Enabled: *input.Enabled, ChangeKinds: changeKinds, MinimumSeverity: input.MinimumSeverity,
		Revision: 1, Epoch: epoch, ApprovedBy: session.User.ID, ApprovedByName: session.User.Name,
		Rationale: input.Rationale, CreatedAt: now, UpdatedAt: now,
		Connection: NotificationPolicyConnection{
			ID: connection.ID, Name: connection.Name, Profile: connection.Profile,
			Revision: connection.Revision, Enabled: connection.Enabled, Current: true,
		},
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("notification_policies")+`
		(id,workspace_id,name,connection_id,connection_profile,connection_revision,enabled,
		 change_kinds,minimum_severity,revision,epoch,approved_by,rationale,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		policy.ID, workspace, policy.Name, policy.ConnectionID, policy.ConnectionProfile,
		policy.ConnectionRevision, policy.Enabled, policy.ChangeKinds, policy.MinimumSeverity,
		policy.Revision, policy.Epoch, policy.ApprovedBy, policy.Rationale, now); err != nil {
		return err
	}
	if err = a.insertNotificationPolicyRevision(r.Context(), tx, policy, session.User.Name); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"policy": policy})
	return nil
}

func (a *Application) readNotificationPolicy(ctx context.Context, workspace, id string,
	db queryRower) (NotificationPolicy, error) {
	return scanNotificationPolicy(db.QueryRow(ctx, `SELECT `+notificationPolicyColumns+
		a.notificationPolicyFrom()+` WHERE p.workspace_id=$1 AND p.id=$2`, workspace, id))
}

func (a *Application) getNotificationPolicy(w http.ResponseWriter, r *http.Request,
	workspace, id string) error {
	policy, err := a.readNotificationPolicy(r.Context(), workspace, id, a.pool)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy})
	return nil
}

func (a *Application) listNotificationPolicies(w http.ResponseWriter, r *http.Request, workspace string) error {
	if r.URL.RawQuery != "" {
		return errInvalid
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("notification_policies")+
		` WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+notificationPolicyColumns+a.notificationPolicyFrom()+
		` WHERE p.workspace_id=$1 ORDER BY p.id`, workspace)
	if err != nil {
		return err
	}
	items := make([]NotificationPolicy, 0, total)
	for rows.Next() {
		policy, scanErr := scanNotificationPolicy(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, policy)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": nil})
	return nil
}

func (a *Application) updateNotificationPolicy(w http.ResponseWriter, r *http.Request,
	workspace string, session authenticatedSession, id string) error {
	var input struct {
		Name            optional[string]   `json:"name"`
		ConnectionID    optional[string]   `json:"connectionId"`
		Enabled         optional[bool]     `json:"enabled"`
		ChangeKinds     optional[[]string] `json:"changeKinds"`
		MinimumSeverity optional[string]   `json:"minimumSeverity"`
		Rationale       string             `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.Rationale, 8192) ||
		(!input.Name.Set && !input.ConnectionID.Set && !input.Enabled.Set &&
			!input.ChangeKinds.Set && !input.MinimumSeverity.Set) {
		return errInvalid
	}
	if input.Name.Set && (input.Name.Value == nil || !validText(*input.Name.Value, 256)) ||
		input.ConnectionID.Set && (input.ConnectionID.Value == nil || !validID(*input.ConnectionID.Value)) ||
		input.Enabled.Set && input.Enabled.Value == nil ||
		input.MinimumSeverity.Set && (input.MinimumSeverity.Value == nil ||
			!validNotificationSeverity(*input.MinimumSeverity.Value)) {
		return errInvalid
	}
	var changeKinds []string
	if input.ChangeKinds.Set {
		if input.ChangeKinds.Value == nil {
			return errInvalid
		}
		var valid bool
		changeKinds, valid = normalizeNotificationChangeKinds(*input.ChangeKinds.Value)
		if !valid {
			return errInvalid
		}
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	epoch, err := a.lockNotificationPolicyEpoch(r.Context(), tx, workspace)
	if err != nil {
		return err
	}
	current, err := scanNotificationPolicy(tx.QueryRow(r.Context(), `SELECT `+notificationPolicyColumns+
		a.notificationPolicyFrom()+` WHERE p.workspace_id=$1 AND p.id=$2 FOR UPDATE OF p`,
		workspace, id))
	if err != nil {
		return err
	}
	next := current
	if input.Name.Set {
		next.Name = *input.Name.Value
	}
	if input.Enabled.Set {
		next.Enabled = *input.Enabled.Value
	}
	if input.ChangeKinds.Set {
		next.ChangeKinds = changeKinds
	}
	if input.MinimumSeverity.Set {
		next.MinimumSeverity = *input.MinimumSeverity.Value
	}
	if input.ConnectionID.Set {
		connection, err := a.notificationPolicyConnection(r.Context(), tx, workspace, *input.ConnectionID.Value)
		if err != nil {
			return err
		}
		next.ConnectionID, next.ConnectionProfile, next.ConnectionRevision =
			connection.ID, connection.Profile, connection.Revision
		next.Connection = NotificationPolicyConnection{
			ID: connection.ID, Name: connection.Name, Profile: connection.Profile,
			Revision: connection.Revision, Enabled: connection.Enabled, Current: true,
		}
	}
	if next.Name == current.Name && next.ConnectionID == current.ConnectionID &&
		next.Enabled == current.Enabled && slices.Equal(next.ChangeKinds, current.ChangeKinds) &&
		next.MinimumSeverity == current.MinimumSeverity {
		return errConflict
	}
	next.Revision, next.Epoch = current.Revision+1, epoch
	next.ApprovedBy, next.ApprovedByName = session.User.ID, session.User.Name
	next.Rationale, next.UpdatedAt = input.Rationale, a.config.Now().UTC()
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("notification_policies")+`
		SET name=$3,connection_id=$4,connection_profile=$5,connection_revision=$6,enabled=$7,
		 change_kinds=$8,minimum_severity=$9,revision=$10,epoch=$11,approved_by=$12,rationale=$13,updated_at=$14
		WHERE workspace_id=$1 AND id=$2`,
		workspace, id, next.Name, next.ConnectionID, next.ConnectionProfile, next.ConnectionRevision,
		next.Enabled, next.ChangeKinds, next.MinimumSeverity, next.Revision, next.Epoch,
		next.ApprovedBy, next.Rationale, next.UpdatedAt); err != nil {
		return err
	}
	if err = a.insertNotificationPolicyRevision(r.Context(), tx, next, session.User.Name); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": next})
	return nil
}

func notificationPolicyEventParameters(r *http.Request) (int, string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", errInvalid
	}
	for key, values := range query {
		if key != "limit" && key != "cursor" || len(values) != 1 {
			return 0, "", errInvalid
		}
	}
	limit := 100
	if values, present := query["limit"]; present {
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			return 0, "", errInvalid
		}
	}
	cursor := ""
	if values, present := query["cursor"]; present {
		cursor = values[0]
		if !validID(cursor) {
			return 0, "", errInvalid
		}
	}
	return limit, cursor, nil
}

func scanNotificationPolicyEvent(row pgx.Row) (NotificationPolicyEvent, error) {
	var event NotificationPolicyEvent
	err := row.Scan(&event.ID, &event.WorkspaceID, &event.PolicyID, &event.PolicyRevision,
		&event.FindingID, &event.FindingChangeRevision, &event.Outcome, &event.DeliveryID,
		&event.CreatedAt)
	if err == nil {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	return event, err
}

func (a *Application) listNotificationPolicyEvents(w http.ResponseWriter, r *http.Request,
	workspace, policyID string) error {
	limit, cursor, err := notificationPolicyEventParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+a.table("notification_policies")+
		` WHERE workspace_id=$1 AND id=$2)`, workspace, policyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	var total int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("notification_policy_events")+
		` WHERE workspace_id=$1 AND policy_id=$2`, workspace, policyID).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT id,workspace_id,policy_id,policy_revision,finding_id,
		finding_change_revision,outcome,delivery_id,created_at
		FROM `+a.table("notification_policy_events")+`
		WHERE workspace_id=$1 AND policy_id=$2 AND id>$3 ORDER BY id LIMIT $4`,
		workspace, policyID, cursor, limit+1)
	if err != nil {
		return err
	}
	items := make([]NotificationPolicyEvent, 0, limit+1)
	for rows.Next() {
		event, scanErr := scanNotificationPolicyEvent(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := items[len(items)-1].ID
		next = &value
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}
