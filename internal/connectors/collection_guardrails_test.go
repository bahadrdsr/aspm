package connectors

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGitLabLinkageAndRedirectFailClosed(t *testing.T) {
	for _, stage := range []string{"pipeline", "job", "artifact-redirect"} {
		t.Run(stage, func(t *testing.T) {
			base, client, calls := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "synthetic-only" {
					t.Error("unexpected method or credential")
				}
				switch r.URL.Path {
				case "/api/v4/projects/31":
					_, _ = io.WriteString(w, `{"id":31,"path_with_namespace":"group/repo"}`)
				case "/api/v4/projects/31/pipelines/7":
					project := "31"
					if stage == "pipeline" {
						project = "999"
					}
					_, _ = io.WriteString(w, `{"id":7,"project_id":`+project+`,"status":"success"}`)
				case "/api/v4/projects/31/jobs/9":
					pipeline := "7"
					if stage == "job" {
						pipeline = "8"
					}
					_, _ = io.WriteString(w, `{"id":9,"pipeline":{"id":`+pipeline+`,"project_id":31}}`)
				case "/api/v4/projects/31/jobs/9/artifacts/report.sarif":
					w.Header().Set("Location", "https://"+r.Host+"/not-authorized")
					w.WriteHeader(302)
				default:
					t.Error("followed a credential redirect or escaped the selected routes")
					w.WriteHeader(403)
				}
			})
			adapter, err := OpenCollector(context.Background(), CollectorConfig{
				Profile: GitLabArtifacts, Endpoint: base, Client: client, Token: "synthetic-only",
			})
			if err != nil {
				t.Fatal(err)
			}
			identity := Identity{"workspace", "source", "run", "scope"}
			result, err := adapter.Collect(context.Background(), CollectRequest{
				Identity: identity, Project: "31", PipelineID: "7", JobID: "9", ArtifactPath: "report.sarif",
				Repository: "unused/out/of/scope", Region: "unused-other-provider-scope",
			})
			wantCalls := map[string]int32{"pipeline": 2, "job": 3, "artifact-redirect": 4}[stage]
			if !errors.Is(err, ErrScope) || result.Complete || result.Identity != identity || len(result.Gaps) == 0 ||
				calls.Load() != wantCalls || len(result.Records) != int(wantCalls)-1 {
				t.Fatalf("linkage/redirect scope or partial records lost: %v", err)
			}
		})
	}
}

func TestContinuationOriginPathAndQueryScope(t *testing.T) {
	current, err := tlsURL("https://api.invalid/repos/owner/repo/code-scanning/alerts?per_page=5&page=1", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{
		"https://other.invalid/repos/owner/repo/code-scanning/alerts?per_page=5&page=2",
		"https://api.invalid:444/repos/owner/repo/code-scanning/alerts?per_page=5&page=2",
		"https://api.invalid/repos/other/repo/code-scanning/alerts?per_page=5&page=2",
		"https://api.invalid/repos/owner%2Frepo/code-scanning/alerts?per_page=5&page=2",
		"https://api.invalid/repos/owner/repo/../other/code-scanning/alerts?per_page=5&page=2",
		"http://api.invalid/repos/owner/repo/code-scanning/alerts?per_page=5&page=2",
		"https://user@api.invalid/repos/owner/repo/code-scanning/alerts?per_page=5&page=2",
		"?per_page=5&page=2#fragment",
	} {
		if _, err := scopedLink(current, next); !errors.Is(err, ErrScope) {
			t.Errorf("unsafe continuation accepted: %q (%v)", next, err)
		}
	}
	expected := url.Values{"per_page": {"5"}, "page": {"2"}}
	for _, query := range []string{"per_page=100&page=2", "per_page=5&page=2&page=3", "per_page=5&page=2&state=all", "per_page=5;page=2"} {
		next := *current
		next.RawQuery = query
		if exactQuery(&next, expected) {
			t.Errorf("query widened the selected request: %q", query)
		}
	}
}

func TestEC2RawBytesAndAccountScope(t *testing.T) {
	instance := `<item> <instanceId>i-0123456789abcdef0</instanceId><unknown note="retained">native context</unknown> </item>`
	raw := `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><reservationSet><item><ownerId>123456789012</ownerId><instancesSet>` + instance + `</instancesSet></item></reservationSet></DescribeInstancesResponse>`
	records, token, err := ec2Page(context.Background(), []byte(raw), "123456789012", "https://ec2.invalid/")
	if err != nil || token != "" || len(records) != 1 || string(records[0].Raw) != instance ||
		records[0].SourceScanAt != nil || records[0].SourceUpdatedAt != nil {
		t.Fatalf("EC2 raw native mapping failed: %v", err)
	}
	if _, _, err := ec2Page(context.Background(), []byte(raw), "000000000000", "https://ec2.invalid/"); !errors.Is(err, ErrScope) {
		t.Fatal("out-of-account EC2 inventory accepted")
	}
	if _, _, err := ec2Page(context.Background(), []byte(`<DescribeInstancesResponse/>`), "123456789012", "https://ec2.invalid/"); !errors.Is(err, ErrProtocol) {
		t.Fatal("missing inventory response field appeared complete")
	}
}

type zipEntry struct {
	name, body string
	mode       fs.FileMode
}

func testArchive(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestZIPExactFileAndBounds(t *testing.T) {
	const file = "reports/report.sarif"
	for _, test := range []struct {
		name    string
		entries []zipEntry
		want    error
	}{
		{"exact-only", []zipEntry{{"prefix/" + file, "wrong file", 0}, {file, "exact raw bytes", 0}}, nil},
		{"duplicate", []zipEntry{{file, "one", 0}, {file, "two", 0}}, ErrScope},
		{"traversal", []zipEntry{{"../outside", "unused", 0}, {file, "report", 0}}, ErrScope},
		{"symlink", []zipEntry{{file, "outside", fs.ModeSymlink | 0o777}}, ErrScope},
		{"missing", []zipEntry{{"prefix/" + file, "not the exact file", 0}}, ErrUnavailable},
		{"expanded-cap", []zipEntry{{file, strings.Repeat("x", 8192), 0}}, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := testArchive(t, test.entries)
			raw, err := boundedZIP(context.Background(), archive, file, 4096)
			if test.want == nil {
				if err != nil || string(raw) != "exact raw bytes" {
					t.Fatalf("exact report changed: %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("unsafe archive handling: got %v, want %v", err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := boundedZIP(ctx, nil, file, 4096); !errors.Is(err, context.Canceled) {
		t.Fatal("ZIP reader ignored context")
	}
}

func TestCloudRecordsDoNotWidenSelectedScope(t *testing.T) {
	subscription := "11111111-1111-4111-8111-111111111111"
	parent := "/subscriptions/" + subscription + "/resourceGroups/group/providers/Microsoft.Compute/virtualMachines/vm"
	id := parent + "/providers/Microsoft.Security/assessments/check"
	raw := `{"id":"` + id + `","name":"check","properties":{"resourceDetails":{"id":"` + parent + `","source":"Azure"},"status":{"code":"Unhealthy"}}}`
	record, err := assessmentRecord([]byte(raw), subscription, "https://arm.invalid/assessments")
	if err != nil || record.Severity != "" || record.SourceScanAt != nil || !bytes.Equal(record.Raw, []byte(raw)) {
		t.Fatalf("native assessment semantics changed: %v", err)
	}
	if _, err := assessmentRecord([]byte(raw), "other-subscription", "https://arm.invalid/assessments"); !errors.Is(err, ErrScope) {
		t.Fatal("cross-subscription assessment accepted")
	}
	request := CollectRequest{AccountID: "123456789012", Region: "us-east-1"}
	asff := `{"SchemaVersion":"2018-10-08","Id":"native-id","ProductArn":"arn:aws:securityhub:us-east-1:123456789012:product/123456789012/check","AwsAccountId":"123456789012","Region":"us-east-1"}`
	record, err = asffRecord([]byte(asff), request, "https://securityhub.invalid/findings")
	if err != nil || record.SourceScanAt != nil || record.Severity != "" {
		t.Fatalf("ASFF invented time or severity: %v", err)
	}
	request.AccountID = "000000000000"
	if _, err := asffRecord([]byte(asff), request, "https://securityhub.invalid/findings"); !errors.Is(err, ErrScope) {
		t.Fatal("cross-account ASFF accepted")
	}
}
