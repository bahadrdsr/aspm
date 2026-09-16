package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type gitlabCollector struct{ collectorBase }

func gitlabProject(value string) bool {
	if numericID.MatchString(value) {
		return true
	}
	segments := strings.Split(value, "/")
	if len(segments) < 2 || len(segments) > 16 {
		return false
	}
	for _, segment := range segments {
		if !nameID.MatchString(segment) {
			return false
		}
	}
	return true
}

func (c *gitlabCollector) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := startCollection(ctx, request.Identity)
	if err != nil {
		return result, err
	}
	if !gitlabProject(request.Project) || !numericID.MatchString(request.PipelineID) ||
		!numericID.MatchString(request.JobID) || !artifactPath(request.ArtifactPath) {
		return result.fail("scope", connectorError(ErrScope))
	}
	headers := http.Header{"PRIVATE-TOKEN": {c.config.Token}, "Accept": {"application/json"}}
	budget := httpBudget{http: c.http}
	target := apiURL(c.base, "api", "v4", "projects", request.Project)
	var project struct {
		ID       json.Number `json:"id"`
		FullPath string      `json:"path_with_namespace"`
	}
	response, err := budget.readJSON(ctx, target, headers, &project)
	if err != nil {
		return result.fail("repository", err)
	}
	projectID := project.ID.String()
	if !numericID.MatchString(projectID) || numericID.MatchString(request.Project) && projectID != request.Project ||
		!numericID.MatchString(request.Project) && project.FullPath != request.Project {
		return result.fail("repository", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{Kind: "repository", ExternalID: projectID, Raw: response.Body, RawURL: target.String()})
	target = apiURL(c.base, "api", "v4", "projects", request.Project, "pipelines", request.PipelineID)
	var pipeline struct {
		ID        json.Number `json:"id"`
		ProjectID json.Number `json:"project_id"`
		Status    string      `json:"status"`
		UpdatedAt string      `json:"updated_at"`
	}
	response, err = budget.readJSON(ctx, target, headers, &pipeline)
	if err != nil {
		return result.fail("pipeline", err)
	}
	if pipeline.ID.String() != request.PipelineID || pipeline.ProjectID.String() != projectID {
		return result.fail("pipeline", connectorError(ErrScope))
	}
	updated, err := sourceTime(pipeline.UpdatedAt)
	if err != nil {
		return result.fail("pipeline", err)
	}
	result.Records = append(result.Records, Record{
		Kind: "pipeline", ExternalID: request.PipelineID, ParentID: projectID, NativeRunID: request.PipelineID,
		State: pipeline.Status, SourceUpdatedAt: updated, Raw: response.Body, RawURL: target.String(),
	})
	target = apiURL(c.base, "api", "v4", "projects", request.Project, "jobs", request.JobID)
	var job struct {
		ID       json.Number `json:"id"`
		Status   string      `json:"status"`
		Pipeline struct {
			ID        json.Number `json:"id"`
			ProjectID json.Number `json:"project_id"`
		} `json:"pipeline"`
	}
	response, err = budget.readJSON(ctx, target, headers, &job)
	if err != nil {
		return result.fail("job", err)
	}
	if job.ID.String() != request.JobID || job.Pipeline.ID.String() != request.PipelineID || job.Pipeline.ProjectID.String() != projectID {
		return result.fail("job", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{
		Kind: "job", ExternalID: request.JobID, ParentID: projectID, NativeRunID: request.PipelineID,
		State: job.Status, Raw: response.Body, RawURL: target.String(),
	})
	segments := []string{"api", "v4", "projects", request.Project, "jobs", request.JobID, "artifacts"}
	segments = append(segments, strings.Split(request.ArtifactPath, "/")...)
	target = apiURL(c.base, segments...)
	response, err = budget.do(ctx, http.MethodGet, target, headers, nil, nil)
	if err != nil {
		return result.fail("report", err)
	}
	if response.Status != http.StatusOK || len(response.Body) == 0 {
		return result.fail("report", connectorError(ErrProtocol))
	}
	result.Records = append(result.Records, Record{
		Kind: "report", ExternalID: request.JobID + ":" + request.ArtifactPath, ParentID: projectID, NativeRunID: request.PipelineID,
		Raw: response.Body, RawURL: target.String(), SourceScanAt: sarifScanTime(response.Body),
	})
	result.Complete = true
	return result, nil
}
