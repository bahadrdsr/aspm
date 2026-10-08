package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	retentionFindingDecisionEvent    = "finding-decision-event"
	retentionNotificationPolicyRev   = "notification-policy-revision"
	retentionFindingChangeEvent      = "finding-change-event"
	retentionNotificationPolicyEvent = "notification-policy-event"
)

type findingDecisionArchivePayload struct {
	SchemaVersion    int      `json:"schemaVersion"`
	ResourceKind     string   `json:"resourceKind"`
	ID               string   `json:"id"`
	WorkspaceID      string   `json:"workspaceId"`
	FindingID        string   `json:"findingId"`
	DecisionRevision int64    `json:"decisionRevision"`
	ActorID          string   `json:"actorId"`
	Action           string   `json:"action"`
	Rationale        string   `json:"rationale"`
	ChangedFields    []string `json:"changedFields"`
	BeforeState      any      `json:"beforeState"`
	AfterState       any      `json:"afterState"`
	CreatedAt        string   `json:"createdAt"`
}

type notificationPolicyRevisionArchivePayload struct {
	SchemaVersion      int      `json:"schemaVersion"`
	ResourceKind       string   `json:"resourceKind"`
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspaceId"`
	PolicyID           string   `json:"policyId"`
	Name               string   `json:"name"`
	ConnectionID       string   `json:"connectionId"`
	ConnectionProfile  string   `json:"connectionProfile"`
	ConnectionRevision int64    `json:"connectionRevision"`
	Enabled            bool     `json:"enabled"`
	ChangeKinds        []string `json:"changeKinds"`
	MinimumSeverity    string   `json:"minimumSeverity"`
	Revision           int64    `json:"revision"`
	Epoch              int64    `json:"epoch"`
	ActorID            string   `json:"actorId"`
	ActorName          string   `json:"actorName"`
	Rationale          string   `json:"rationale"`
	CreatedAt          string   `json:"createdAt"`
}

type findingChangeArchivePayload struct {
	SchemaVersion  int     `json:"schemaVersion"`
	ResourceKind   string  `json:"resourceKind"`
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspaceId"`
	FindingID      string  `json:"findingId"`
	PolicyEpoch    int64   `json:"policyEpoch"`
	Title          string  `json:"title"`
	Severity       string  `json:"severity"`
	AssetName      string  `json:"assetName"`
	ChangeKind     string  `json:"changeKind"`
	ChangeRevision int64   `json:"changeRevision"`
	ChangeAt       string  `json:"changeAt"`
	State          string  `json:"state"`
	WorkerID       *string `json:"workerId"`
	Fence          int64   `json:"fence"`
	LeaseUntil     *string `json:"leaseUntil"`
}

type notificationPolicyEventArchivePayload struct {
	SchemaVersion         int     `json:"schemaVersion"`
	ResourceKind          string  `json:"resourceKind"`
	ID                    string  `json:"id"`
	WorkspaceID           string  `json:"workspaceId"`
	PolicyID              string  `json:"policyId"`
	PolicyRevision        int64   `json:"policyRevision"`
	FindingID             string  `json:"findingId"`
	FindingChangeRevision int64   `json:"findingChangeRevision"`
	Outcome               string  `json:"outcome"`
	DeliveryID            *string `json:"deliveryId"`
	CreatedAt             string  `json:"createdAt"`
}

func isHistoryRetentionKind(kind string) bool {
	switch kind {
	case retentionFindingDecisionEvent, retentionNotificationPolicyRev,
		retentionFindingChangeEvent, retentionNotificationPolicyEvent:
		return true
	default:
		return false
	}
}

func validRetentionHoldKind(kind string) bool {
	return kind == "import" || kind == "observation" || kind == "correlation-event" ||
		isHistoryRetentionKind(kind)
}

func historyRetentionReasons(hold, current, pending, active bool) []string {
	reasons := []string{}
	if hold {
		reasons = append(reasons, "legal-hold")
	}
	if current {
		reasons = append(reasons, "current-policy-revision")
	}
	if pending {
		reasons = append(reasons, "pending-policy-evaluation")
	}
	if active {
		reasons = append(reasons, "active-delivery")
	}
	return reasons
}

func historyPayloadBinding(payload []byte, authority ...string) string {
	sum := sha256.Sum256(payload)
	return strings.Join(append([]string{hex.EncodeToString(sum[:])}, authority...), "|")
}

func canonicalJSONValue(raw []byte) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func nullableRetentionTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return timeString(*value)
}

func addHistoryRetentionItem(snapshot *retentionSnapshot, item retentionSnapshotItem) error {
	addRetentionItem(snapshot, item)
	if len(snapshot.Items) > retentionPreviewLimit {
		return errTooLarge
	}
	return nil
}

func (a *Application) appendHistoryRetention(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	if err := a.appendFindingDecisionHistory(ctx, db, workspace, cutoff, snapshot); err != nil {
		return err
	}
	if err := a.appendNotificationPolicyRevisionHistory(ctx, db, workspace, cutoff, snapshot); err != nil {
		return err
	}
	if err := a.appendFindingChangeHistory(ctx, db, workspace, cutoff, snapshot); err != nil {
		return err
	}
	return a.appendNotificationPolicyEventHistory(ctx, db, workspace, cutoff, snapshot)
}

func (a *Application) appendFindingDecisionHistory(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT e.id,e.workspace_id,e.finding_id,e.decision_revision,
		e.actor_id,e.action,e.rationale,e.changed_fields,e.before_state,e.after_state,e.created_at,
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=e.workspace_id AND h.resource_kind='finding-decision-event'
		 AND h.resource_id=e.id AND h.released_at IS NULL),'')
		FROM `+a.table("finding_decision_events")+` e
		WHERE e.workspace_id=$1 AND e.created_at<=$2
		ORDER BY e.created_at,e.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload findingDecisionArchivePayload
		var changed, before, after []byte
		var created time.Time
		var holds string
		if err = rows.Scan(&payload.ID, &payload.WorkspaceID, &payload.FindingID,
			&payload.DecisionRevision, &payload.ActorID, &payload.Action, &payload.Rationale,
			&changed, &before, &after, &created, &holds); err != nil {
			return err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, retentionFindingDecisionEvent
		if err = json.Unmarshal(changed, &payload.ChangedFields); err != nil {
			return err
		}
		if payload.BeforeState, err = canonicalJSONValue(before); err != nil {
			return err
		}
		if payload.AfterState, err = canonicalJSONValue(after); err != nil {
			return err
		}
		payload.CreatedAt = timeString(created)
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		item := retentionSnapshotItem{RetentionPreviewItem: RetentionPreviewItem{
			Class: "audit", ResourceKind: retentionFindingDecisionEvent, ResourceID: payload.ID,
			Action: "archive-audit", ObservedAt: created.UTC(), SizeBytes: int64(len(data)),
			ProtectedReasons: historyRetentionReasons(holds != "", false, false, false),
		}}
		item.binding = historyPayloadBinding(data, holds)
		if err = addHistoryRetentionItem(snapshot, item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (a *Application) appendNotificationPolicyRevisionHistory(ctx context.Context, db retentionDB,
	workspace string, cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT r.id,r.workspace_id,r.policy_id,r.name,r.connection_id,
		r.connection_profile,r.connection_revision,r.enabled,r.change_kinds,r.minimum_severity,
		r.revision,r.epoch,r.actor_id,r.actor_name,r.rationale,r.created_at,
		EXISTS(SELECT 1 FROM `+a.table("notification_policies")+` p
		 WHERE p.workspace_id=r.workspace_id AND p.id=r.policy_id AND p.revision=r.revision),
		COALESCE((SELECT string_agg(e.id||':'||e.policy_epoch::text||':'||e.state||':'||
		 COALESCE(e.worker_id,'')||':'||e.fence::text||':'||
		 COALESCE(extract(epoch FROM e.lease_until)::text,''),',' ORDER BY e.id)
		 FROM `+a.table("finding_change_events")+` e
		 WHERE e.workspace_id=r.workspace_id AND e.state IN ('pending','processing')
		 AND r.epoch<=e.policy_epoch
		 AND NOT EXISTS(SELECT 1 FROM `+a.table("notification_policy_revisions")+` later
		  WHERE later.workspace_id=r.workspace_id AND later.policy_id=r.policy_id
		  AND later.epoch>r.epoch AND later.epoch<=e.policy_epoch)),''),
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=r.workspace_id AND h.resource_kind='notification-policy-revision'
		 AND h.resource_id=r.id AND h.released_at IS NULL),'')
		FROM `+a.table("notification_policy_revisions")+` r
		WHERE r.workspace_id=$1 AND r.created_at<=$2
		ORDER BY r.created_at,r.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload notificationPolicyRevisionArchivePayload
		var created time.Time
		var current bool
		var pending, holds string
		if err = rows.Scan(&payload.ID, &payload.WorkspaceID, &payload.PolicyID, &payload.Name,
			&payload.ConnectionID, &payload.ConnectionProfile, &payload.ConnectionRevision,
			&payload.Enabled, &payload.ChangeKinds, &payload.MinimumSeverity, &payload.Revision,
			&payload.Epoch, &payload.ActorID, &payload.ActorName, &payload.Rationale, &created,
			&current, &pending, &holds); err != nil {
			return err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, retentionNotificationPolicyRev
		payload.CreatedAt = timeString(created)
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		item := retentionSnapshotItem{RetentionPreviewItem: RetentionPreviewItem{
			Class: "audit", ResourceKind: retentionNotificationPolicyRev, ResourceID: payload.ID,
			Action: "archive-audit", ObservedAt: created.UTC(), SizeBytes: int64(len(data)),
			ProtectedReasons: historyRetentionReasons(holds != "", current, pending != "", false),
		}}
		item.binding = historyPayloadBinding(data, boolString(current), pending, holds)
		if err = addHistoryRetentionItem(snapshot, item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (a *Application) appendFindingChangeHistory(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT e.id,e.workspace_id,e.finding_id,e.policy_epoch,e.title,
		e.severity,e.asset_name,e.change_kind,e.change_revision,e.change_at,e.state,
		e.worker_id,e.fence,e.lease_until,
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=e.workspace_id AND h.resource_kind='finding-change-event'
		 AND h.resource_id=e.id AND h.released_at IS NULL),'')
		FROM `+a.table("finding_change_events")+` e
		WHERE e.workspace_id=$1 AND e.change_at<=$2
		ORDER BY e.change_at,e.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload findingChangeArchivePayload
		var changed time.Time
		var lease *time.Time
		var holds string
		if err = rows.Scan(&payload.ID, &payload.WorkspaceID, &payload.FindingID,
			&payload.PolicyEpoch, &payload.Title, &payload.Severity, &payload.AssetName,
			&payload.ChangeKind, &payload.ChangeRevision, &changed, &payload.State,
			&payload.WorkerID, &payload.Fence, &lease, &holds); err != nil {
			return err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, retentionFindingChangeEvent
		payload.ChangeAt = timeString(changed)
		if lease != nil {
			value := timeString(*lease)
			payload.LeaseUntil = &value
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		pending := payload.State == "pending" || payload.State == "processing"
		item := retentionSnapshotItem{RetentionPreviewItem: RetentionPreviewItem{
			Class: "audit", ResourceKind: retentionFindingChangeEvent, ResourceID: payload.ID,
			Action: "archive-audit", ObservedAt: changed.UTC(), SizeBytes: int64(len(data)),
			ProtectedReasons: historyRetentionReasons(holds != "", false, pending, false),
		}}
		item.binding = historyPayloadBinding(data, holds)
		if err = addHistoryRetentionItem(snapshot, item); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (a *Application) appendNotificationPolicyEventHistory(ctx context.Context, db retentionDB,
	workspace string, cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT e.id,e.workspace_id,e.policy_id,e.policy_revision,
		e.finding_id,e.finding_change_revision,e.outcome,e.delivery_id,e.created_at,
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=e.workspace_id AND h.resource_kind='notification-policy-event'
		 AND h.resource_id=e.id AND h.released_at IS NULL),''),
		COALESCE(d.state,''),COALESCE(d.worker_id,''),COALESCE(d.fence,0),
		d.lease_until,d.dispatch_started_at,d.completed_at
		FROM `+a.table("notification_policy_events")+` e
		LEFT JOIN `+a.table("finding_deliveries")+` d
		ON d.workspace_id=e.workspace_id AND d.id=e.delivery_id
		WHERE e.workspace_id=$1 AND e.created_at<=$2
		ORDER BY e.created_at,e.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload notificationPolicyEventArchivePayload
		var created time.Time
		var holds, deliveryState, worker string
		var fence int64
		var lease, dispatch, completed *time.Time
		if err = rows.Scan(&payload.ID, &payload.WorkspaceID, &payload.PolicyID,
			&payload.PolicyRevision, &payload.FindingID, &payload.FindingChangeRevision,
			&payload.Outcome, &payload.DeliveryID, &created, &holds, &deliveryState,
			&worker, &fence, &lease, &dispatch, &completed); err != nil {
			return err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, retentionNotificationPolicyEvent
		payload.CreatedAt = timeString(created)
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		active := deliveryState == "queued" || deliveryState == "dispatching"
		item := retentionSnapshotItem{RetentionPreviewItem: RetentionPreviewItem{
			Class: "audit", ResourceKind: retentionNotificationPolicyEvent, ResourceID: payload.ID,
			Action: "archive-audit", ObservedAt: created.UTC(), SizeBytes: int64(len(data)),
			ProtectedReasons: historyRetentionReasons(holds != "", false, false, active),
		}}
		deliveryAuthority := strings.Join([]string{
			deliveryState, worker, intString(fence), nullableRetentionTime(lease),
			nullableRetentionTime(dispatch), nullableRetentionTime(completed),
		}, ":")
		item.binding = historyPayloadBinding(data, holds, deliveryAuthority)
		if err = addHistoryRetentionItem(snapshot, item); err != nil {
			return err
		}
	}
	return rows.Err()
}
