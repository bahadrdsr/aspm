package connectors

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

var fixtureCredentials = aws.Credentials{
	AccessKeyID: "SYNTHETIC_ACCESS_ONLY", SecretAccessKey: "synthetic-secret-not-real", SessionToken: "synthetic-session-not-real",
}

func checkAWSSignature(t *testing.T, r *http.Request, body []byte, service string) {
	t.Helper()
	stamp, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		t.Error("AWS API fixture did not receive a SigV4 signing time")
		return
	}
	check(t, r.Header.Get("X-Amz-Security-Token") == fixtureCredentials.SessionToken, "AWS session credentials were not used")
	original := r.Header.Get("Authorization")
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.URL.Scheme, request.URL.Host = "https", r.Host
	request.Header.Del("Authorization")
	err = v4.NewSigner().SignHTTP(r.Context(), fixtureCredentials, request, fmt.Sprintf("%x", sha256.Sum256(body)), service, "us-east-1", stamp)
	check(t, err == nil && original == request.Header.Get("Authorization"), "AWS request failed independent SDK SigV4 recomputation")
}

func TestM09AWSSignedInventoryAndFindings(t *testing.T) {
	request := collectionRequest()
	product := "arn:aws:securityhub:us-east-1:123456789012:product/123456789012/synthetic"
	finding := `{"SchemaVersion":"2018-10-08","Id":"synthetic-finding","ProductArn":"` + product + `","AwsAccountId":"123456789012","Region":"us-east-1","GeneratorId":"synthetic-generator","Title":"Synthetic check","Description":"Synthetic evidence only.","CreatedAt":"2026-09-01T08:00:00Z","UpdatedAt":"2026-09-02T08:00:00Z","Severity":{"Label":"HIGH"},"Resources":[{"Type":"AwsEc2Instance","Id":"i-0123456789abcdef0","Region":"us-east-1"}],"UserDefinedFields":{"synthetic-note":"preserve"}}`
	base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
		check(t, r.Method == "POST", "AWS profile must use signed Query/REST POSTs, not resource mutations")
		body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		check(t, err == nil && len(body) <= 65536, "AWS request exceeded fixture bounds")
		_ = r.Body.Close()
		switch r.URL.Path {
		case "/":
			checkAWSSignature(t, r, body, "ec2")
			form, err := url.ParseQuery(string(body))
			check(t, err == nil && form.Get("Action") == "DescribeInstances" && form.Get("Version") == "2016-11-15" && form.Get("MaxResults") == "5", "AWS inventory is not the bounded EC2 Query profile")
			check(t, form.Get("Filter.1.Name") == "owner-id" && form.Get("Filter.1.Value.1") == request.AccountID, "EC2 inventory lost its approved account filter")
			w.Header().Set("Content-Type", "text/xml")
			if form.Get("NextToken") == "" {
				_, _ = io.WriteString(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>synthetic-request</requestId><reservationSet><item><ownerId>123456789012</ownerId><instancesSet><item><instanceId>i-0123456789abcdef0</instanceId><instanceType>t3.micro</instanceType></item></instancesSet></item></reservationSet><nextToken>ec2-next</nextToken></DescribeInstancesResponse>`)
			} else {
				check(t, form.Get("NextToken") == "ec2-next", "EC2 continuation token changed")
				_, _ = io.WriteString(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>synthetic-request-2</requestId><reservationSet/></DescribeInstancesResponse>`)
			}
		case "/findings":
			checkAWSSignature(t, r, body, "securityhub")
			r.Body = io.NopCloser(bytes.NewReader(body))
			payload := jsonBody(t, r)
			check(t, at(t, payload, "MaxResults") == float64(5), "Security Hub page must be bounded")
			accounts, accountOK := at(t, payload, "Filters", "AwsAccountId").([]any)
			regions, regionOK := at(t, payload, "Filters", "Region").([]any)
			check(t, accountOK && len(accounts) == 1 && regionOK && len(regions) == 1, "Security Hub filters must not add accounts or regions")
			check(t, at(t, payload, "Filters", "AwsAccountId", 0, "Value") == request.AccountID && at(t, payload, "Filters", "AwsAccountId", 0, "Comparison") == "EQUALS", "Security Hub account filter missing")
			check(t, at(t, payload, "Filters", "Region", 0, "Value") == request.Region && at(t, payload, "Filters", "Region", 0, "Comparison") == "EQUALS", "cross-region aggregation must not broaden collection")
			if at(t, payload, "NextToken") == nil || at(t, payload, "NextToken") == "" {
				reply(w, 200, `{"Findings":[`+finding+`],"NextToken":"hub-next"}`)
			} else {
				check(t, at(t, payload, "NextToken") == "hub-next", "Security Hub continuation changed")
				reply(w, 200, `{"Findings":[]}`)
			}
		default:
			t.Error("AWS collector called an undeclared API")
			w.WriteHeader(404)
		}
	})
	config := collectorConfig("aws-commercial-ec2-securityhub", base, client)
	config.Credentials = credentials.NewStaticCredentialsProvider(fixtureCredentials.AccessKeyID, fixtureCredentials.SecretAccessKey, fixtureCredentials.SessionToken)
	result := collected(t, openCollector(t, config), request)
	record(t, result, "resource", "i-0123456789abcdef0")
	item := record(t, result, "finding", "synthetic-finding")
	check(t, item.ParentID == product && item.Severity == "HIGH" && bytes.Equal(item.Raw, []byte(finding)), "ASFF product identity, severity or original fields lost")
	check(t, item.SourceScanAt == nil && item.SourceUpdatedAt != nil && calls.Load() == 4, "AWS pagination/time semantics were broadened or fabricated")
}

func TestM09AzureResourceGraphAndAssessments(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("other-subscription-next=%t", outside), func(t *testing.T) {
			request := collectionRequest()
			scope := "/subscriptions/" + request.SubscriptionID
			resourceID := scope + "/resourceGroups/synthetic/providers/Microsoft.Compute/virtualMachines/vm1"
			assessmentID := resourceID + "/providers/Microsoft.Security/assessments/synthetic-check"
			raw := `{"id":"` + assessmentID + `","name":"synthetic-check","type":"Microsoft.Security/assessments","properties":{"resourceDetails":{"source":"Azure","id":"` + resourceID + `"},"displayName":"Synthetic check","status":{"code":"Unhealthy"},"additionalData":{"synthetic-note":"preserve"}}}`
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Header.Get("Authorization") == "Bearer "+syntheticToken, "Azure ARM bearer mapping missing")
				switch r.Method + " " + r.URL.Path {
				case "POST /providers/Microsoft.ResourceGraph/resources":
					check(t, r.URL.Query().Get("api-version") == "2022-10-01", "Resource Graph version differs from the declared profile")
					body := jsonBody(t, r)
					subscriptions, ok := at(t, body, "subscriptions").([]any)
					check(t, ok && len(subscriptions) == 1 && at(t, body, "managementGroups") == nil, "Resource Graph query broadened beyond one subscription")
					check(t, at(t, body, "subscriptions", 0) == request.SubscriptionID && at(t, body, "query") == "Resources | project id, name, type, location, subscriptionId", "Resource Graph scope/query broadened")
					check(t, at(t, body, "options", "$top") == float64(5) && at(t, body, "options", "resultFormat") == "objectArray", "Resource Graph page/result profile missing")
					if at(t, body, "options", "$skipToken") == nil || at(t, body, "options", "$skipToken") == "" {
						reply(w, 200, `{"totalRecords":2,"count":1,"resultTruncated":"true","$skipToken":"rg-next","data":[{"id":"`+resourceID+`","name":"vm1","type":"microsoft.compute/virtualmachines","subscriptionId":"`+request.SubscriptionID+`"}]}`)
					} else {
						check(t, at(t, body, "options", "$skipToken") == "rg-next", "Resource Graph continuation changed")
						reply(w, 200, `{"totalRecords":2,"count":1,"resultTruncated":"false","data":[{"id":"`+resourceID+`-2","name":"vm1-2","type":"microsoft.compute/virtualmachines","subscriptionId":"`+request.SubscriptionID+`"}]}`)
					}
				case "GET " + scope + "/providers/Microsoft.Security/assessments":
					check(t, r.URL.Query().Get("api-version") == "2020-01-01", "Assessments List must use its documented API profile")
					if r.URL.Query().Get("$skiptoken") != "" {
						check(t, r.URL.Query().Get("$skiptoken") == "assessment-next", "assessment continuation changed")
						reply(w, 200, `{"value":[]}`)
					} else {
						nextScope := scope
						if outside {
							nextScope = "/subscriptions/other-subscription"
						}
						reply(w, 200, `{"value":[`+raw+`],"nextLink":"https://`+r.Host+nextScope+`/providers/Microsoft.Security/assessments?api-version=2020-01-01&$skiptoken=assessment-next"}`)
					}
				default:
					t.Error("Azure collector left the declared subscription/API scope")
					w.WriteHeader(404)
				}
			})
			adapter := openCollector(t, collectorConfig("azure-public-resourcegraph-assessments", base, client))
			if outside {
				result, err := adapter.Collect(boundedContext(t), request)
				check(t, errors.Is(err, ErrScope) && !result.Complete && len(result.Gaps) > 0 && calls.Load() == 3, "assessment nextLink must not broaden subscription scope")
			} else {
				result := collected(t, adapter, request)
				record(t, result, "resource", resourceID)
				record(t, result, "resource", resourceID+"-2")
				item := record(t, result, "assessment", assessmentID)
				check(t, item.ParentID == resourceID && item.State == "Unhealthy" && bytes.Equal(item.Raw, []byte(raw)), "Defender assessment/ARM identity or raw meaning lost")
				check(t, item.SourceScanAt == nil && calls.Load() == 4, "Azure collection invented scan time or exceeded its declared pages")
			}
		})
	}
}
