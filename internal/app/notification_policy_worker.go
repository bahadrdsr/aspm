package app

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type findingChangeEventRecord struct {
	ID, WorkspaceID, FindingID, Title, Severity, AssetName, ChangeKind, State string
	PolicyEpoch, ChangeRevision, Fence                                        int64
	ChangeAt                                                                  time.Time
	WorkerID                                                                  *string
	LeaseUntil                                                                *time.Time
}

func scanFindingChangeEvent(row pgx.Row) (findingChangeEventRecord, error) {
	var event findingChangeEventRecord
	err := row.Scan(&event.ID, &event.WorkspaceID, &event.FindingID, &event.PolicyEpoch,
		&event.Title, &event.Severity, &event.AssetName, &event.ChangeKind,
		&event.ChangeRevision, &event.ChangeAt, &event.State, &event.WorkerID,
		&event.Fence, &event.LeaseUntil)
	return event, err
}

const findingChangeEventColumns = `id,workspace_id,finding_id,policy_epoch,title,severity,
	asset_name,change_kind,change_revision,change_at,state,worker_id,fence,lease_until`

const findingChangeEventReturning = `e.id,e.workspace_id,e.finding_id,e.policy_epoch,e.title,e.severity,
	e.asset_name,e.change_kind,e.change_revision,e.change_at,e.state,e.worker_id,e.fence,e.lease_until`

func (w *DeliveryWorker) claimFindingChangeEvent(ctx context.Context) (findingChangeEventRecord, bool, error) {
	event, err := scanFindingChangeEvent(w.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+w.table("finding_change_events")+`
		WHERE state='pending' OR (state='processing' AND lease_until<=clock_timestamp())
		ORDER BY change_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
		UPDATE `+w.table("finding_change_events")+` e
		SET state='processing',worker_id=$1,fence=e.fence+1,
			lease_until=clock_timestamp()+($2*interval '1 millisecond')
		FROM candidate c WHERE e.id=c.id RETURNING `+findingChangeEventReturning,
		w.workerID, w.lease.Milliseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return event, false, nil
	}
	if err != nil {
		return event, false, deliveryInfrastructureError(ctx, "claim notification-policy event failed")
	}
	return event, true, nil
}

func scanNotificationPolicyRevision(row pgx.Row) (notificationPolicyRevision, error) {
	var revision notificationPolicyRevision
	err := row.Scan(&revision.ID, &revision.WorkspaceID, &revision.PolicyID, &revision.Name,
		&revision.ConnectionID, &revision.ConnectionProfile, &revision.ConnectionRevision,
		&revision.Enabled, &revision.ChangeKinds, &revision.MinimumSeverity, &revision.Revision,
		&revision.Epoch, &revision.ActorID, &revision.ActorName, &revision.Rationale,
		&revision.CreatedAt)
	return revision, err
}

func notificationSeverityRank(value string) int {
	switch value {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func notificationPolicyMatches(revision notificationPolicyRevision, event findingChangeEventRecord) bool {
	return revision.Enabled && slices.Contains(revision.ChangeKinds, event.ChangeKind) &&
		notificationSeverityRank(event.Severity) >= notificationSeverityRank(revision.MinimumSeverity)
}

func notificationPolicyApprovalRef(workspace string, revision notificationPolicyRevision) string {
	return "aspm:notification-policy:" + workspace + ":" + revision.PolicyID + ":" +
		strconv.FormatInt(revision.Revision, 10)
}

func notificationPolicyKey(revision notificationPolicyRevision, event findingChangeEventRecord) string {
	return "notification-policy:" + revision.PolicyID + ":" + event.FindingID + ":" +
		strconv.FormatInt(event.ChangeRevision, 10)
}

func (w *DeliveryWorker) policyFindingDelivery(event findingChangeEventRecord,
	revision notificationPolicyRevision, connection integrationConnectionRecord) (FindingDelivery, bool) {
	if w.publicOrigin == "" || !validText(event.Title, 1024) || !validText(event.AssetName, 256) ||
		!validNotificationSeverity(event.Severity) || !slices.Contains(notificationChangeKinds, event.ChangeKind) {
		return FindingDelivery{}, false
	}
	policyID, policyRevision, changeRevision :=
		revision.PolicyID, revision.Revision, event.ChangeRevision
	delivery := FindingDelivery{
		ID: newID(), WorkspaceID: event.WorkspaceID, FindingID: event.FindingID,
		ConnectionID: connection.ID, ConnectionRevision: connection.Revision,
		Profile: connection.Profile, Channel: connection.Channel, RequestedBy: revision.ActorID,
		State: "queued", CreatedAt: w.now().UTC(), Jira: connection.Jira,
		TriggerKind: "notification-policy", PolicyID: &policyID,
		PolicyRevision: &policyRevision, FindingChangeRevision: &changeRevision,
		Payload: FindingNotification{
			Title: event.Title,
			Body: "Severity: " + event.Severity + "\nAsset: " + event.AssetName +
				"\nChange: " + event.ChangeKind,
			DeepLink: w.publicOrigin + "/#/work?finding=" + url.QueryEscape(event.FindingID),
		},
	}
	switch delivery.Profile {
	case connectors.SlackWorkspaceBot:
		if !integrationChannel.MatchString(delivery.Channel) || delivery.Jira != nil || connection.Teams != nil {
			return FindingDelivery{}, false
		}
	case connectors.JiraCloudV3:
		if !validJiraTarget(delivery.Jira) || delivery.Channel != "" ||
			utf8.RuneCountInString(delivery.Payload.Title) > 255 {
			return FindingDelivery{}, false
		}
		delivery.Payload.Fields = make(map[string]string, len(delivery.Jira.FieldMappings))
		for field, source := range delivery.Jira.FieldMappings {
			switch source {
			case "finding.id":
				delivery.Payload.Fields[field] = event.FindingID
			case "finding.title":
				delivery.Payload.Fields[field] = event.Title
			case "finding.severity":
				delivery.Payload.Fields[field] = event.Severity
			case "asset.name":
				delivery.Payload.Fields[field] = event.AssetName
			case "finding.deepLink":
				delivery.Payload.Fields[field] = delivery.Payload.DeepLink
			}
		}
	case connectors.TeamsWorkflows:
		if delivery.Jira != nil || delivery.Channel != "" || !validTeamsMetadata(connection.Teams) {
			return FindingDelivery{}, false
		}
		delivery.Destination = &TeamsDestination{connection.Name, *connection.Teams}
		if !teamsPayloadValid(delivery.Payload) {
			return FindingDelivery{}, false
		}
	default:
		return FindingDelivery{}, false
	}
	return delivery, true
}

func (w *DeliveryWorker) insertNotificationPolicyEvent(ctx context.Context, tx pgx.Tx,
	event findingChangeEventRecord, revision notificationPolicyRevision,
	outcome string, deliveryID *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO `+w.table("notification_policy_events")+`
		(id,workspace_id,policy_id,policy_revision,finding_id,finding_change_revision,
		 outcome,delivery_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		newID(), event.WorkspaceID, revision.PolicyID, revision.Revision, event.FindingID,
		event.ChangeRevision, outcome, deliveryID, w.now().UTC())
	return err
}

func (w *DeliveryWorker) evaluateFindingChangeEvent(ctx context.Context,
	claimed findingChangeEventRecord) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return deliveryInfrastructureError(ctx, "open notification-policy evaluation failed")
	}
	defer rollback(tx)
	event, err := scanFindingChangeEvent(tx.QueryRow(ctx, `SELECT `+findingChangeEventColumns+`
		FROM `+w.table("finding_change_events")+`
		WHERE workspace_id=$1 AND id=$2 AND state='processing' AND worker_id=$3 AND fence=$4
		AND lease_until>clock_timestamp() FOR UPDATE`,
		claimed.WorkspaceID, claimed.ID, w.workerID, claimed.Fence))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryLeaseLost
	}
	if err != nil {
		return deliveryInfrastructureError(ctx, "read claimed notification-policy event failed")
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (policy_id)
		id,workspace_id,policy_id,name,connection_id,connection_profile,connection_revision,
		enabled,change_kinds,minimum_severity,revision,epoch,actor_id,actor_name,rationale,created_at
		FROM `+w.table("notification_policy_revisions")+`
		WHERE workspace_id=$1 AND epoch<=$2 ORDER BY policy_id,epoch DESC`,
		event.WorkspaceID, event.PolicyEpoch)
	if err != nil {
		return deliveryInfrastructureError(ctx, "read notification-policy revisions failed")
	}
	revisions := []notificationPolicyRevision{}
	for rows.Next() {
		revision, scanErr := scanNotificationPolicyRevision(rows)
		if scanErr != nil {
			rows.Close()
			return deliveryInfrastructureError(ctx, "scan notification-policy revision failed")
		}
		revisions = append(revisions, revision)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return deliveryInfrastructureError(ctx, "finish notification-policy revisions failed")
	}
	for _, revision := range revisions {
		if !notificationPolicyMatches(revision, event) {
			continue
		}
		connection, connectionErr := scanIntegrationConnection(tx.QueryRow(ctx,
			`SELECT `+integrationConnectionColumns+` FROM `+w.table("integration_connections")+`
			WHERE workspace_id=$1 AND id=$2 FOR SHARE`,
			event.WorkspaceID, revision.ConnectionID))
		if errors.Is(connectionErr, pgx.ErrNoRows) ||
			connectionErr == nil && (!connection.Enabled || connection.Profile != revision.ConnectionProfile ||
				connection.Revision != revision.ConnectionRevision) {
			if err = w.insertNotificationPolicyEvent(ctx, tx, event, revision, "connection-stale", nil); err != nil {
				return deliveryInfrastructureError(ctx, "record stale notification-policy outcome failed")
			}
			continue
		}
		if connectionErr != nil {
			return deliveryInfrastructureError(ctx, "read notification-policy connection failed")
		}
		if connection.Profile == connectors.JiraCloudV3 {
			if err = lockJiraFindingEffect(ctx, tx, event.WorkspaceID,
				connection.ID, event.FindingID); err != nil {
				return deliveryInfrastructureError(ctx, "lock Jira finding effect failed")
			}
			existing, effectErr := w.jiraFindingEffect(ctx, tx, event.WorkspaceID,
				connection.ID, event.FindingID)
			if effectErr == nil {
				if err = w.insertNotificationPolicyEvent(ctx, tx, event, revision,
					"duplicate-ticket", &existing); err != nil {
					return deliveryInfrastructureError(ctx, "record duplicate Jira notification-policy outcome failed")
				}
				continue
			}
			if !errors.Is(effectErr, pgx.ErrNoRows) {
				return deliveryInfrastructureError(ctx, "read Jira finding effect failed")
			}
		}
		delivery, valid := w.policyFindingDelivery(event, revision, connection)
		if !valid {
			if err = w.insertNotificationPolicyEvent(ctx, tx, event, revision, "invalid-payload", nil); err != nil {
				return deliveryInfrastructureError(ctx, "record invalid notification-policy payload failed")
			}
			continue
		}
		var deliveryID *string
		outcome := "queued"
		if deliveryID == nil {
			key := notificationPolicyKey(revision, event)
			digest, digestErr := deliveryIntentBinding(delivery, key)
			if digestErr != nil {
				return digestErr
			}
			record, insertErr := w.insertFindingDelivery(ctx, tx, delivery, key, digest,
				notificationPolicyApprovalRef(event.WorkspaceID, revision))
			if insertErr != nil {
				return deliveryInfrastructureError(ctx, "queue notification-policy delivery failed")
			}
			deliveryID = &record.ID
			if delivery.Profile == connectors.JiraCloudV3 {
				if err = w.insertJiraFindingEffect(ctx, tx, delivery); err != nil {
					return deliveryInfrastructureError(ctx, "reserve Jira finding effect failed")
				}
			}
		}
		if err = w.insertNotificationPolicyEvent(ctx, tx, event, revision, outcome, deliveryID); err != nil {
			return deliveryInfrastructureError(ctx, "record notification-policy outcome failed")
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE `+w.table("finding_change_events")+`
		SET state='evaluated',worker_id=NULL,lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND state='processing' AND worker_id=$3 AND fence=$4
		AND lease_until>clock_timestamp()`,
		event.WorkspaceID, event.ID, w.workerID, event.Fence)
	if err != nil {
		return deliveryInfrastructureError(ctx, "complete notification-policy event failed")
	}
	if tag.RowsAffected() != 1 {
		return deliveryLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return deliveryInfrastructureError(ctx, "commit notification-policy evaluation failed")
	}
	return nil
}

func (w *DeliveryWorker) processNextFindingChangeEvent(ctx context.Context) (bool, error) {
	event, processed, err := w.claimFindingChangeEvent(ctx)
	if err != nil || !processed {
		return processed, err
	}
	if err = w.evaluateFindingChangeEvent(ctx, event); err != nil {
		return true, err
	}
	return true, nil
}
