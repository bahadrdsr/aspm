package connectors

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const githubAlert = `{"number":%d,"created_at":"2026-09-01T08:00:00Z","updated_at":"2026-09-02T08:00:00Z","state":"open","rule":{"id":"synthetic-rule","severity":"warning","security_severity_level":"high"},"tool":{"name":"synthetic-scanner"},"most_recent_instance":{"ref":"refs/heads/main","analysis_key":"synthetic-analysis","location":{"path":"src/synthetic.go","start_line":12}},"vendorNote":"preserve synthetic source context"}`
const sarifReport = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"synthetic-scanner"}},"invocations":[{"startTime":"2026-09-01T09:00:00Z","executionSuccessful":true}],"results":[{"ruleId":"synthetic-rule","level":"warning","message":{"text":"Synthetic fixture only."}}]}]}`

func TestM09GitHubRepositoriesAndAlerts(t *testing.T) {
	base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
		check(t, r.Method == "GET" && r.Header.Get("Authorization") == "Bearer "+syntheticToken, "GitHub collection must be read-only and installation-token authenticated")
		check(t, r.Header.Get("X-GitHub-Api-Version") == "2026-03-10" && r.Header.Get("Accept") == "application/vnd.github+json", "GitHub profile headers missing")
		switch r.URL.Path {
		case "/repos/owner/repo":
			reply(w, 200, `{"id":101,"full_name":"owner/repo","owner":{"id":102,"login":"owner"}}`)
		case "/repos/owner/repo/code-scanning/alerts":
			check(t, r.URL.Query().Get("per_page") == "5", "GitHub alert page is unbounded")
			number := 7
			if r.URL.Query().Get("page") == "2" {
				number = 8
			} else {
				w.Header().Set("Link", `<https://`+r.Host+`/repos/owner/repo/code-scanning/alerts?per_page=5&page=2>; rel="next"`)
			}
			reply(w, 200, "["+fmt.Sprintf(githubAlert, number)+"]")
		default:
			t.Error("GitHub collector left the selected repository/read-only routes")
			w.WriteHeader(404)
		}
	})
	adapter := openCollector(t, collectorConfig("github-cloud-app", base, client))
	request := collectionRequest()
	first := collected(t, adapter, request)
	record(t, first, "repository", "101")
	alert := record(t, first, "finding", "7")
	record(t, first, "finding", "8")
	check(t, alert.ParentID == "101" && alert.Severity == "high" && alert.Location == "src/synthetic.go:12", "GitHub native alert mapping lost scope, severity or location")
	check(t, alert.SourceScanAt == nil && alert.SourceUpdatedAt != nil && alert.SourceUpdatedAt.Format(time.RFC3339) == "2026-09-02T08:00:00Z", "alert updated time was substituted for unknown scan time")
	check(t, bytes.Equal(alert.Raw, []byte(fmt.Sprintf(githubAlert, 7))), "GitHub collection rewrote/dropped raw alert fields")
	again := collected(t, adapter, request)
	repeated := record(t, again, "finding", "7")
	check(t, again.Identity == first.Identity && repeated.SourceScanAt == nil && bytes.Equal(repeated.Raw, alert.Raw), "unchanged polling invented a new source scan")
	check(t, calls.Load() == 6, "GitHub pagination/refetch budget was not explicit")
}

func TestM09GitLabPipelineArtifact(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-artifact=%t", missing), func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Method == "GET" && r.Header.Get("PRIVATE-TOKEN") == syntheticToken, "GitLab read_api token/GET mapping missing")
				switch r.URL.Path {
				case "/api/v4/projects/31":
					reply(w, 200, `{"id":31,"path_with_namespace":"synthetic/repo"}`)
				case "/api/v4/projects/31/pipelines/7":
					reply(w, 200, `{"id":7,"project_id":31,"status":"success","finished_at":"2026-09-02T10:00:00Z"}`)
				case "/api/v4/projects/31/jobs/9":
					reply(w, 200, `{"id":9,"status":"success","pipeline":{"id":7,"project_id":31},"artifacts":[{"file_type":"archive","filename":"artifacts.zip"}]}`)
				case "/api/v4/projects/31/jobs/9/artifacts/reports/report.sarif":
					if missing {
						reply(w, 404, `{"message":"synthetic artifact expired"}`)
					} else {
						reply(w, 200, sarifReport)
					}
				default:
					t.Error("GitLab collector enumerated or mutated outside its selected project/pipeline/job")
					w.WriteHeader(404)
				}
			})
			adapter := openCollector(t, collectorConfig("gitlab-com-artifacts-v4", base, client))
			request := collectionRequest()
			if missing {
				result, err := adapter.Collect(boundedContext(t), request)
				check(t, errors.Is(err, ErrUnavailable) && !result.Complete && len(result.Gaps) > 0, "expired GitLab artifact must be an explicit coverage gap")
			} else {
				result := collected(t, adapter, request)
				record(t, result, "repository", "31")
				record(t, result, "pipeline", "7")
				report := record(t, result, "report", "9:reports/report.sarif")
				check(t, report.ParentID == "31" && report.NativeRunID == "7" && bytes.Equal(report.Raw, []byte(sarifReport)), "GitLab artifact lost native run identity or exact report")
				check(t, report.SourceScanAt != nil && report.SourceScanAt.Format(time.RFC3339) == "2026-09-01T09:00:00Z", "GitLab replaced declared SARIF scan time with pipeline finish/download time")
			}
			check(t, calls.Load() == 4, "selected GitLab profile must make four bounded reads, not discover/run pipelines")
		})
	}
}

func reportZIP(t *testing.T, data string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("reports/report.sarif")
	requireOK(t, err)
	_, err = file.Write([]byte(data))
	requireOK(t, err)
	requireOK(t, writer.Close())
	return buffer.Bytes()
}

func TestM09AzureDevOpsBuildArtifact(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("out-of-scope-download=%t", outside), func(t *testing.T) {
			request := collectionRequest()
			request.Repository = "22222222-2222-4222-8222-222222222222"
			request.Project = "33333333-3333-4333-8333-333333333333"
			prefix := "/org/" + request.Project + "/_apis"
			report := strings.Replace(sarifReport, `"invocations":[{"startTime":"2026-09-01T09:00:00Z","executionSuccessful":true}],`, "", 1)
			archive := reportZIP(t, report)
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				check(t, r.Method == "GET" && ok && user == "" && password == syntheticToken && r.URL.Query().Get("api-version") == "7.1", "Azure DevOps PAT/read-only/version profile missing")
				switch r.URL.Path {
				case prefix + "/git/repositories/" + request.Repository:
					reply(w, 200, `{"id":"`+request.Repository+`","name":"synthetic-repo","project":{"id":"`+request.Project+`"}}`)
				case prefix + "/build/builds/81":
					reply(w, 200, `{"id":81,"status":"completed","result":"succeeded","finishTime":"2026-09-02T10:00:00Z","repository":{"id":"`+request.Repository+`"}}`)
				case prefix + "/build/builds/81/artifacts":
					check(t, r.URL.Query().Get("artifactName") == "aspm-report", "Azure DevOps artifact lookup broadened")
					if r.URL.Query().Get("$format") == "zip" {
						w.Header().Set("Content-Type", "application/zip")
						_, _ = w.Write(archive)
					} else {
						project := request.Project
						if outside {
							project = "other-project"
						}
						reply(w, 200, `{"id":9,"name":"aspm-report","resource":{"type":"Container","downloadUrl":"https://`+r.Host+`/org/`+project+`/_apis/build/builds/81/artifacts?artifactName=aspm-report&api-version=7.1&$format=zip"}}`)
					}
				default:
					t.Error("Azure DevOps artifact URL escaped approved organization/project/build")
					w.WriteHeader(404)
				}
			})
			adapter := openCollector(t, collectorConfig("ado-services-build-artifacts", base, client))
			if outside {
				result, err := adapter.Collect(boundedContext(t), request)
				check(t, errors.Is(err, ErrScope) && !result.Complete && calls.Load() == 3, "unsafe artifact URL must be rejected before a credentialed fetch")
			} else {
				result := collected(t, adapter, request)
				record(t, result, "repository", request.Repository)
				record(t, result, "pipeline", "81")
				item := record(t, result, "report", "9:reports/report.sarif")
				check(t, item.NativeRunID == "81" && item.ParentID == request.Repository && bytes.Equal(item.Raw, []byte(report)), "build artifact ZIP extraction lost exact report/run scope")
				check(t, item.SourceScanAt == nil && calls.Load() == 4, "build finish must not become scan time or trigger fanout")
			}
		})
	}
}

func TestM09CollectionBoundsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"authentication", ErrAuth}, {"rate-limit", ErrRateLimited}, {"page-cap", ErrLimit},
		{"request-cap", ErrLimit}, {"foreign-next", ErrScope}, {"other-repository-next", ErrScope}, {"body-cap", ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Method == "GET", "collection attempted a mutation")
				if r.URL.Path == "/repos/owner/repo" {
					reply(w, 200, `{"id":101,"full_name":"owner/repo"}`)
					return
				}
				check(t, r.URL.Path == "/repos/owner/repo/code-scanning/alerts" && r.URL.Query().Get("page") != "2", "collector fetched a forbidden/beyond-budget continuation")
				switch tc.name {
				case "authentication":
					reply(w, 401, `{"message":"synthetic token revoked"}`)
				case "rate-limit":
					w.Header().Set("Retry-After", "5")
					reply(w, 429, `{"message":"synthetic quota"}`)
				default:
					next := "https://" + r.Host + "/repos/owner/repo/code-scanning/alerts?per_page=5&page=2"
					if tc.name == "foreign-next" {
						next = "https://outside.invalid/repos/owner/repo/code-scanning/alerts?page=2"
					} else if tc.name == "other-repository-next" {
						next = "https://" + r.Host + "/repos/other/repo/code-scanning/alerts?page=2"
					}
					w.Header().Set("Link", "<"+next+">; rel=\"next\"")
					reply(w, 200, "["+fmt.Sprintf(githubAlert, 7)+"]")
				}
			})
			config := collectorConfig("github-cloud-app", base, client)
			if tc.name == "page-cap" {
				config.Limits.Pages = 1
			}
			if tc.name == "request-cap" {
				config.Limits.Requests = 1
			}
			if tc.name == "body-cap" {
				config.Limits.Bytes = 128
			}
			request := collectionRequest()
			result, err := openCollector(t, config).Collect(boundedContext(t), request)
			check(t, errors.Is(err, tc.want) && !result.Complete && len(result.Gaps) > 0 && result.Identity == request.Identity, "partial collection lost its explicit error, coverage gap or identity")
			record(t, result, "repository", "101")
			wantCalls := int32(2)
			if tc.name == "request-cap" {
				wantCalls = 1
			}
			check(t, calls.Load() == wantCalls, "collection exceeded the request budget or followed an unsafe URL")
			if tc.name == "page-cap" {
				check(t, result.Continuation["alerts"] != "", "bounded pagination must expose its unfinished feed cursor")
			}
			if tc.name == "rate-limit" {
				check(t, result.RetryAfter == 5*time.Second, "collection lost Retry-After")
			}
		})
	}
}
