package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/bahadrdsr/aspm/internal/connectors"
)

type TeamsMetadata struct {
	WorkflowOrigin        string `json:"workflowOrigin"`
	ChannelType           string `json:"channelType"`
	OwnershipAcknowledged bool   `json:"ownershipAcknowledged"`
}

type TeamsDestination struct {
	Name string `json:"name"`
	TeamsMetadata
}

func (target *TeamsMetadata) UnmarshalJSON(data []byte) error {
	type value TeamsMetadata
	var decoded value
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*target = TeamsMetadata(decoded)
	return nil
}

func (target *TeamsDestination) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Name                  string `json:"name"`
		WorkflowOrigin        string `json:"workflowOrigin"`
		ChannelType           string `json:"channelType"`
		OwnershipAcknowledged bool   `json:"ownershipAcknowledged"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*target = TeamsDestination{decoded.Name, TeamsMetadata{
		decoded.WorkflowOrigin, decoded.ChannelType, decoded.OwnershipAcknowledged,
	}}
	return nil
}

type teamsConfiguration struct {
	ChannelType           string `json:"channelType"`
	OwnershipAcknowledged bool   `json:"ownershipAcknowledged"`
}

func (config *teamsConfiguration) UnmarshalJSON(data []byte) error {
	type value teamsConfiguration
	var decoded value
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*config = teamsConfiguration(decoded)
	return nil
}

func validTeamsConfiguration(config *teamsConfiguration) bool {
	return config != nil && config.ChannelType == "standard" && config.OwnershipAcknowledged
}

func validTeamsOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Host != strings.ToLower(u.Host) ||
		strings.HasSuffix(u.Hostname(), ".") || origin != "https://"+u.Host {
		return false
	}
	if _, err := ValidateDeliveryGateway(origin); err != nil {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number == 443 || strconv.Itoa(number) != port {
			return false
		}
	}
	return true
}

func teamsWorkflowOrigin(value string) (string, bool) {
	u, err := url.Parse(value)
	if err != nil || u == nil || !validIntegrationToken(value) || u.Scheme != "https" ||
		u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" ||
		len(u.Path) > 4096 || strings.ContainsAny(u.Path, "\\%?#") ||
		strings.ContainsFunc(u.Path, unicode.IsControl) || strings.Contains(u.Path, "//") {
		return "", false
	}
	origin := "https://" + u.Host
	if !validTeamsOrigin(origin) || !strings.HasPrefix(value, origin+"/") {
		return "", false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", false
		}
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["sig"]) != 1 || query["sig"][0] == "" ||
		strings.ContainsFunc(query["sig"][0], unicode.IsSpace) {
		return "", false
	}
	for key, values := range query {
		if strings.ContainsFunc(key, unicode.IsControl) {
			return "", false
		}
		for _, value := range values {
			if strings.ContainsFunc(value, unicode.IsControl) {
				return "", false
			}
		}
	}
	return origin, true
}

func validTeamsMetadata(target *TeamsMetadata) bool {
	return target != nil && validTeamsOrigin(target.WorkflowOrigin) &&
		target.ChannelType == "standard" && target.OwnershipAcknowledged
}

func validTeamsDestination(target *TeamsDestination) bool {
	return target != nil && validText(target.Name, 256) && validTeamsMetadata(&target.TeamsMetadata)
}

func (a *Application) writeTeamsPreview(w http.ResponseWriter, delivery FindingDelivery, digest string) {
	writeJSON(w, http.StatusOK, map[string]any{"preview": struct {
		WorkspaceID        string              `json:"workspaceId"`
		FindingID          string              `json:"findingId"`
		ConnectionID       string              `json:"connectionId"`
		ConnectionRevision int64               `json:"connectionRevision"`
		Profile            string              `json:"profile"`
		RequestedBy        string              `json:"requestedBy"`
		Destination        *TeamsDestination   `json:"destination"`
		Payload            FindingNotification `json:"payload"`
		BindingDigest      string              `json:"bindingDigest"`
		NativeValidation   string              `json:"nativeValidation"`
		ReviewRequirements []string            `json:"reviewRequirements"`
	}{
		delivery.WorkspaceID, delivery.FindingID, delivery.ConnectionID, delivery.ConnectionRevision,
		delivery.Profile, delivery.RequestedBy, delivery.Destination, delivery.Payload, digest,
		"not-run", []string{"explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"},
	}})
}

func teamsPayloadValid(payload FindingNotification) bool {
	_, err := connectors.TeamsWorkflowPayload(connectors.Action{
		Title: payload.Title, Body: payload.Body, DeepLink: payload.DeepLink,
	})
	return err == nil && strings.TrimSpace(payload.Title) != "" && payload.Fields == nil
}
