package connectors

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestM09ReviewGitHub403QuotaAndPermissionRemainDistinct(t *testing.T) {
	for _, mode := range []string{"exhausted-primary", "secondary-limit", "permission-denied"} {
		t.Run(mode, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo" {
					reply(w, 200, `{"id":101,"full_name":"owner/repo"}`)
					return
				}
				if r.URL.Path != "/repos/owner/repo/code-scanning/alerts" {
					t.Error("quota handling left the selected repository")
				}
				switch mode {
				case "exhausted-primary":
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10))
					reply(w, 403, `{"message":"API rate limit exceeded for the synthetic fixture"}`)
				case "secondary-limit":
					w.Header().Set("Retry-After", "4")
					reply(w, 403, `{"message":"You have exceeded a secondary rate limit."}`)
				default:
					reply(w, 403, `{"message":"Resource not accessible by integration"}`)
				}
			})
			request := collectionRequest()
			result, err := openCollector(t, collectorConfig("github-cloud-app", base, client)).Collect(boundedContext(t), request)
			want := ErrRateLimited
			if mode == "permission-denied" {
				want = ErrAuth
			}
			check(t, errors.Is(err, want), "GitHub quota and permission errors were conflated")
			check(t, result.Identity == request.Identity && !result.Complete && len(result.Gaps) > 0, "partial collection lost scope or its failure state")
			check(t, len(result.Records) == 1 && result.Records[0].ExternalID == "101", "quota failure discarded valid repository evidence")
			check(t, calls.Load() == 2, "quota handling retried or made additional requests")
			switch mode {
			case "exhausted-primary":
				check(t, result.RetryAfter > 0 && result.RetryAfter <= 31*time.Second, "primary quota reset time was not retained as a bounded retry delay")
			case "secondary-limit":
				check(t, result.RetryAfter == 4*time.Second, "secondary quota Retry-After was lost")
			default:
				check(t, result.RetryAfter == 0, "permission failure invented a retry delay")
			}
		})
	}
}

func TestM09ReviewAzureAssessmentParentScopes(t *testing.T) {
	for _, mode := range []string{"subscription", "resource-group", "foreign-subscription", "foreign-resource-group"} {
		t.Run(mode, func(t *testing.T) {
			request := collectionRequest()
			selected := "/subscriptions/" + request.SubscriptionID
			parent := selected
			foreign := mode == "foreign-subscription" || mode == "foreign-resource-group"
			if foreign {
				parent = "/subscriptions/22222222-2222-4222-8222-222222222222"
			}
			if mode == "resource-group" || mode == "foreign-resource-group" {
				parent += "/resourceGroups/synthetic-group"
			}
			assessmentID := parent + "/providers/Microsoft.Security/assessments/synthetic-scope-check"
			raw := `{"id":"` + assessmentID + `","name":"synthetic-scope-check","type":"Microsoft.Security/assessments","properties":{"resourceDetails":{"source":"Azure","id":"` + parent + `"},"status":{"code":"Unhealthy"},"displayName":"Synthetic scope control"}}`
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/providers/Microsoft.ResourceGraph/resources":
					reply(w, 200, `{"totalRecords":0,"count":0,"resultTruncated":"false","data":[]}`)
				case selected + "/providers/Microsoft.Security/assessments":
					reply(w, 200, `{"value":[`+raw+`]}`)
				default:
					t.Error("assessment collection left the selected endpoint scope")
					w.WriteHeader(404)
				}
			})
			result, err := openCollector(t, collectorConfig("azure-public-resourcegraph-assessments", base, client)).Collect(boundedContext(t), request)
			check(t, result.Identity == request.Identity && calls.Load() == 2, "assessment changed scope or request bounds")
			if foreign {
				check(t, errors.Is(err, ErrScope) && !result.Complete && len(result.Gaps) > 0, "foreign assessment parent was accepted")
				return
			}
			requireOK(t, err)
			check(t, result.Complete && len(result.Records) == 1, "valid selected subscription/resource-group assessment was rejected")
			if len(result.Records) == 1 {
				r := result.Records[0]
				check(t, r.ExternalID == assessmentID && r.ParentID == parent && r.State == "Unhealthy", "assessment identity/parent/state was changed")
				check(t, bytes.Equal(r.Raw, []byte(raw)), "native assessment bytes were not retained")
			}
		})
	}
}
