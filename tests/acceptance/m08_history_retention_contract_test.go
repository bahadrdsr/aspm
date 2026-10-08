package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

type v23FindingDecisionArchivePayload struct {
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

type v23NotificationPolicyRevisionArchivePayload struct {
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

type v23FindingChangeArchivePayload struct {
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

type v23NotificationPolicyEventArchivePayload struct {
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

func v23ArchiveTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func v23CanonicalJSON(t testing.TB, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("canonical V23 JSON fixture is invalid: %v", err)
	}
	return value
}

func v23ArchiveBytes(t testing.TB, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("canonical V23 archive payload could not be encoded: %v", err)
	}
	return data
}

func v23ArchiveDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestM08_V23CanonicalHistoryArchivePayloadsAreFixed(t *testing.T) {
	worker, lease := "policy-worker-1", "2024-01-04T05:07:07.008Z"
	delivery := "99999999999999999999999999999999"
	decisionState := func(workflow string) map[string]any {
		return map[string]any{
			"acceptedRiskExpiresAt": nil,
			"disposition":           "none",
			"dispositionRationale":  "",
			"dispositionScope":      "",
			"ownerId":               "33333333333333333333333333333333",
			"suppressionExpiresAt":  nil,
			"workflowState":         workflow,
		}
	}
	vectors := []struct {
		name, want, digest string
		size               int
		value              any
	}{
		{
			name: "finding-decision-event",
			value: v23FindingDecisionArchivePayload{
				SchemaVersion: 1, ResourceKind: "finding-decision-event",
				ID: "11111111111111111111111111111111", WorkspaceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				FindingID: "22222222222222222222222222222222", DecisionRevision: 2,
				ActorID: "33333333333333333333333333333333", Action: "update",
				Rationale: "Move to pending retest café.", ChangedFields: []string{"workflowState"},
				BeforeState: decisionState("open"), AfterState: decisionState("pending-retest"),
				CreatedAt: "2024-01-02T03:04:05.006Z",
			},
			want: `{"schemaVersion":1,"resourceKind":"finding-decision-event","id":"11111111111111111111111111111111","workspaceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","findingId":"22222222222222222222222222222222","decisionRevision":2,"actorId":"33333333333333333333333333333333","action":"update","rationale":"Move to pending retest café.","changedFields":["workflowState"],"beforeState":{"acceptedRiskExpiresAt":null,"disposition":"none","dispositionRationale":"","dispositionScope":"","ownerId":"33333333333333333333333333333333","suppressionExpiresAt":null,"workflowState":"open"},"afterState":{"acceptedRiskExpiresAt":null,"disposition":"none","dispositionRationale":"","dispositionScope":"","ownerId":"33333333333333333333333333333333","suppressionExpiresAt":null,"workflowState":"pending-retest"},"createdAt":"2024-01-02T03:04:05.006Z"}`,
			size: 825, digest: "sha256:758b157580e3617f10c39d79f94b54bec8f9ca221f63a9cb42956e104a5ce138",
		},
		{
			name: "notification-policy-revision",
			value: v23NotificationPolicyRevisionArchivePayload{
				SchemaVersion: 1, ResourceKind: "notification-policy-revision",
				ID: "44444444444444444444444444444444", WorkspaceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PolicyID: "55555555555555555555555555555555", Name: "High severity history",
				ConnectionID:      "66666666666666666666666666666666",
				ConnectionProfile: "slack-workspace-bot", ConnectionRevision: 3, Enabled: true,
				ChangeKinds: []string{"new", "changed"}, MinimumSeverity: "high", Revision: 2, Epoch: 7,
				ActorID: "33333333333333333333333333333333", ActorName: "Synthetic Admin",
				Rationale: "Keep reviewed policy history.", CreatedAt: "2024-01-03T04:05:06.007Z",
			},
			want: `{"schemaVersion":1,"resourceKind":"notification-policy-revision","id":"44444444444444444444444444444444","workspaceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policyId":"55555555555555555555555555555555","name":"High severity history","connectionId":"66666666666666666666666666666666","connectionProfile":"slack-workspace-bot","connectionRevision":3,"enabled":true,"changeKinds":["new","changed"],"minimumSeverity":"high","revision":2,"epoch":7,"actorId":"33333333333333333333333333333333","actorName":"Synthetic Admin","rationale":"Keep reviewed policy history.","createdAt":"2024-01-03T04:05:06.007Z"}`,
			size: 599, digest: "sha256:48ef083b4bde1bbfe79c2eed1885b2ef4cc4999858bd5c829711f3d000b839c6",
		},
		{
			name: "finding-change-event",
			value: v23FindingChangeArchivePayload{
				SchemaVersion: 1, ResourceKind: "finding-change-event",
				ID: "77777777777777777777777777777777", WorkspaceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				FindingID: "22222222222222222222222222222222", PolicyEpoch: 7,
				Title: "Synthetic finding", Severity: "high", AssetName: "Owned repository",
				ChangeKind: "changed", ChangeRevision: 4, ChangeAt: "2024-01-04T05:06:07.008Z",
				State: "processing", WorkerID: &worker, Fence: 9, LeaseUntil: &lease,
			},
			want: `{"schemaVersion":1,"resourceKind":"finding-change-event","id":"77777777777777777777777777777777","workspaceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","findingId":"22222222222222222222222222222222","policyEpoch":7,"title":"Synthetic finding","severity":"high","assetName":"Owned repository","changeKind":"changed","changeRevision":4,"changeAt":"2024-01-04T05:06:07.008Z","state":"processing","workerId":"policy-worker-1","fence":9,"leaseUntil":"2024-01-04T05:07:07.008Z"}`,
			size: 466, digest: "sha256:a43a5291296cfdd20925c6b5c214ecaeacbdede44ab9308e988fef71148863d8",
		},
		{
			name: "notification-policy-event",
			value: v23NotificationPolicyEventArchivePayload{
				SchemaVersion: 1, ResourceKind: "notification-policy-event",
				ID: "88888888888888888888888888888888", WorkspaceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PolicyID: "55555555555555555555555555555555", PolicyRevision: 2,
				FindingID: "22222222222222222222222222222222", FindingChangeRevision: 4,
				Outcome: "queued", DeliveryID: &delivery, CreatedAt: "2024-01-05T06:07:08.009Z",
			},
			want: `{"schemaVersion":1,"resourceKind":"notification-policy-event","id":"88888888888888888888888888888888","workspaceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policyId":"55555555555555555555555555555555","policyRevision":2,"findingId":"22222222222222222222222222222222","findingChangeRevision":4,"outcome":"queued","deliveryId":"99999999999999999999999999999999","createdAt":"2024-01-05T06:07:08.009Z"}`,
			size: 395, digest: "sha256:1ca122609197edadc79a3a77bc7f27b7311c8d880b9d497e4b25093f72975cba",
		},
	}
	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			data := v23ArchiveBytes(t, vector.value)
			if string(data) != vector.want {
				t.Fatal("canonical V23 archive payload shape or field order changed")
			}
			if len(data) != vector.size {
				t.Fatalf("canonical V23 archive payload size=%d, want %d", len(data), vector.size)
			}
			if got := v23ArchiveDigest(data); got != vector.digest {
				t.Fatalf("canonical V23 archive payload digest=%s, want %s", got, vector.digest)
			}
		})
	}
}
