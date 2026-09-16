package connectors

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type adoCollector struct{ collectorBase }

func (c *adoCollector) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := startCollection(ctx, request.Identity)
	if err != nil {
		return result, err
	}
	if !nameID.MatchString(request.Organization) || !uuidID.MatchString(request.Project) ||
		!uuidID.MatchString(request.Repository) || !numericID.MatchString(request.BuildID) ||
		!nameID.MatchString(request.ArtifactName) || !artifactPath(request.ArtifactPath) {
		return result.fail("scope", connectorError(ErrScope))
	}
	headers := http.Header{"Accept": {"application/json"}, "Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(":"+c.config.Token))}}
	budget := httpBudget{http: c.http}
	prefix := []string{request.Organization, request.Project, "_apis"}
	repoParts := append(append([]string{}, prefix...), "git", "repositories", request.Repository)
	target := withQuery(apiURL(c.base, repoParts...), url.Values{"api-version": {"7.1"}})
	var repository struct {
		ID      string `json:"id"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	response, err := budget.readJSON(ctx, target, headers, &repository)
	if err != nil {
		return result.fail("repository", err)
	}
	if !strings.EqualFold(repository.ID, request.Repository) || !strings.EqualFold(repository.Project.ID, request.Project) {
		return result.fail("repository", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{Kind: "repository", ExternalID: repository.ID, Raw: response.Body, RawURL: target.String()})
	buildParts := append(append([]string{}, prefix...), "build", "builds", request.BuildID)
	target = withQuery(apiURL(c.base, buildParts...), url.Values{"api-version": {"7.1"}})
	var build struct {
		ID         json.Number `json:"id"`
		Status     string      `json:"status"`
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	response, err = budget.readJSON(ctx, target, headers, &build)
	if err != nil {
		return result.fail("pipeline", err)
	}
	if build.ID.String() != request.BuildID || !strings.EqualFold(build.Repository.ID, repository.ID) ||
		build.Project.ID != "" && !strings.EqualFold(build.Project.ID, request.Project) {
		return result.fail("pipeline", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{
		Kind: "pipeline", ExternalID: request.BuildID, ParentID: repository.ID, NativeRunID: request.BuildID,
		State: build.Status, Raw: response.Body, RawURL: target.String(),
	})
	artifactParts := append(append([]string{}, buildParts...), "artifacts")
	target = withQuery(apiURL(c.base, artifactParts...), url.Values{"artifactName": {request.ArtifactName}, "api-version": {"7.1"}})
	var artifact struct {
		ID       json.Number `json:"id"`
		Name     string      `json:"name"`
		Resource struct {
			DownloadURL string `json:"downloadUrl"`
		} `json:"resource"`
	}
	response, err = budget.readJSON(ctx, target, headers, &artifact)
	if err != nil {
		return result.fail("artifact", err)
	}
	if !numericID.MatchString(artifact.ID.String()) || artifact.Name != request.ArtifactName {
		return result.fail("artifact", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{
		Kind: "artifact", ExternalID: artifact.ID.String(), ParentID: repository.ID, NativeRunID: request.BuildID,
		Raw: response.Body, RawURL: target.String(),
	})
	download, err := scopedLink(target, artifact.Resource.DownloadURL)
	if err != nil {
		return result.fail("report", err)
	}
	if !exactQuery(download, url.Values{"artifactName": {request.ArtifactName}, "api-version": {"7.1"}, "$format": {"zip"}}) {
		return result.fail("report", connectorError(ErrScope))
	}
	response, err = budget.do(ctx, http.MethodGet, download, headers, nil, nil)
	if err != nil {
		return result.fail("report", err)
	}
	if response.Status != http.StatusOK {
		return result.fail("report", connectorError(ErrProtocol))
	}
	raw, err := boundedZIP(ctx, response.Body, request.ArtifactPath, c.http.limits.Bytes)
	if err != nil {
		return result.fail("report", err)
	}
	result.Records = append(result.Records, Record{
		Kind: "report", ExternalID: artifact.ID.String() + ":" + request.ArtifactPath,
		ParentID: repository.ID, NativeRunID: request.BuildID, Raw: raw, RawURL: download.String(),
		SourceScanAt: sarifScanTime(raw),
	})
	result.Complete = true
	return result, nil
}

func boundedZIP(ctx context.Context, data []byte, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, connectorError(err)
	}
	if limit < 1 || int64(len(data)) > limit {
		return nil, connectorError(ErrLimit)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, connectorError(ErrProtocol)
	}
	if len(archive.File) > 4096 {
		return nil, connectorError(ErrLimit)
	}
	var selected *zip.File
	for _, file := range archive.File {
		if !artifactPath(strings.TrimSuffix(file.Name, "/")) {
			return nil, connectorError(ErrScope)
		}
		if file.Name == name {
			if selected != nil || !file.Mode().IsRegular() || file.Flags&1 != 0 {
				return nil, connectorError(ErrScope)
			}
			selected = file
		}
	}
	if selected == nil {
		return nil, connectorError(ErrUnavailable)
	}
	if selected.UncompressedSize64 > uint64(limit) {
		return nil, connectorError(ErrLimit)
	}
	reader, err := selected.Open()
	if err != nil {
		return nil, connectorError(ErrProtocol)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, limit+1))
	if ctx.Err() != nil {
		return nil, connectorError(ctx.Err())
	}
	if int64(len(raw)) > limit {
		return nil, connectorError(ErrLimit)
	}
	if err != nil || len(raw) == 0 {
		return nil, connectorError(ErrProtocol)
	}
	return raw, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
