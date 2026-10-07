package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"unicode"
)

var (
	sourceAzureDevOpsOrganization = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
	sourceAzureDevOpsUUID         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sourceAzureDevOpsBuild        = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
)

type AzureDevOpsTarget struct {
	Organization string `json:"organization"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
}

type AzureDevOpsSelection struct {
	BuildID      string `json:"buildId"`
	ArtifactName string `json:"artifactName"`
	ArtifactPath string `json:"artifactPath"`
}

func normalizeAzureDevOpsTarget(target AzureDevOpsTarget) (AzureDevOpsTarget, bool) {
	if !sourceAzureDevOpsOrganization.MatchString(target.Organization) ||
		!sourceAzureDevOpsUUID.MatchString(target.ProjectID) ||
		!sourceAzureDevOpsUUID.MatchString(target.RepositoryID) {
		return AzureDevOpsTarget{}, false
	}
	target.Organization = strings.ToLower(target.Organization)
	target.ProjectID = strings.ToLower(target.ProjectID)
	target.RepositoryID = strings.ToLower(target.RepositoryID)
	return target, true
}

func validAzureDevOpsSelection(selection AzureDevOpsSelection) bool {
	if !sourceAzureDevOpsBuild.MatchString(selection.BuildID) ||
		!sourceAzureDevOpsOrganization.MatchString(selection.ArtifactName) ||
		selection.ArtifactPath == "" || len(selection.ArtifactPath) > 2048 ||
		strings.HasPrefix(selection.ArtifactPath, "/") ||
		strings.HasSuffix(selection.ArtifactPath, "/") ||
		strings.ContainsAny(selection.ArtifactPath, "\\%?#:") ||
		strings.Contains(selection.ArtifactPath, "//") ||
		strings.ContainsFunc(selection.ArtifactPath, unicode.IsControl) ||
		path.Clean(selection.ArtifactPath) != selection.ArtifactPath {
		return false
	}
	segments := strings.Split(selection.ArtifactPath, "/")
	if len(segments) > 32 {
		return false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func sameAzureDevOpsTarget(left, right *AzureDevOpsTarget) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func azureDevOpsScopeKey(target AzureDevOpsTarget) string {
	return target.Organization + "/" + target.ProjectID + "/" + target.RepositoryID
}

func azureDevOpsScopeIdentity(target AzureDevOpsTarget) string {
	data, _ := json.Marshal([]string{target.Organization, target.ProjectID, target.RepositoryID})
	sum := sha256.Sum256(data)
	return "ado-scope-v1:" + hex.EncodeToString(sum[:])
}

func azureDevOpsImportIdentities(target AzureDevOpsTarget, selection AzureDevOpsSelection) (string, string) {
	sourceData, _ := json.Marshal([]string{"ado-source-v1", target.Organization, target.ProjectID, target.RepositoryID})
	sourceSum := sha256.Sum256(sourceData)
	scanData, _ := json.Marshal([]string{"sarif-2.1.0", target.Organization, target.ProjectID, target.RepositoryID,
		selection.BuildID, selection.ArtifactName, selection.ArtifactPath})
	scanSum := sha256.Sum256(scanData)
	return "ado-services-build-artifacts:" + hex.EncodeToString(sourceSum[:]),
		"ado-report-v1:" + hex.EncodeToString(scanSum[:])
}
