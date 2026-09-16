package connectors

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestNativeCredentialBoundaryRejectsMissingTokensBeforeRequests(t *testing.T) {
	for _, profile := range []string{
		"github-cloud-app", "gitlab-com-artifacts-v4", "ado-services-build-artifacts", "azure-public-resourcegraph-assessments",
	} {
		t.Run(profile, func(t *testing.T) {
			if Production.OpenCollector == nil {
				t.Fatal("production collector binding missing")
			}
			base, client, calls := endpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("missing caller credential reached an external tool boundary")
				w.WriteHeader(401)
			})
			config := collectorConfig(profile, base, client)
			config.Token = ""
			collector, err := Production.OpenCollector(boundedContext(t), config)
			if err == nil {
				if collector == nil {
					t.Fatal("missing-credential construction returned nil without error")
				}
				_, err = collector.Collect(boundedContext(t), collectionRequest())
			}
			check(t, errors.Is(err, ErrAuth), "missing caller token did not produce an authentication error")
			check(t, calls.Load() == 0, "missing credential attempted unsigned or ambient authentication")
		})
	}
}

func TestNativeSigningBoundaryRejectsUnavailableAWSIdentity(t *testing.T) {
	for _, mode := range []string{"no-provider", "provider-error", "empty-access-key", "empty-secret", "expired-identity"} {
		t.Run(mode, func(t *testing.T) {
			if Production.OpenCollector == nil {
				t.Fatal("production collector binding missing")
			}
			base, client, calls := endpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("unavailable signing identity reached a native request")
				w.WriteHeader(403)
			})
			config := collectorConfig("aws-commercial-ec2-securityhub", base, client)
			if mode != "no-provider" {
				config.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					identity := aws.Credentials{AccessKeyID: "synthetic-explicit-key", SecretAccessKey: "synthetic-explicit-secret"}
					switch mode {
					case "provider-error":
						return aws.Credentials{}, errors.New("controlled signing identity unavailable")
					case "empty-access-key":
						identity.AccessKeyID = ""
					case "empty-secret":
						identity.SecretAccessKey = ""
					case "expired-identity":
						identity.CanExpire, identity.Expires = true, time.Now().Add(-time.Minute)
					}
					return identity, nil
				})
			}
			collector, err := Production.OpenCollector(boundedContext(t), config)
			if err == nil {
				if collector == nil {
					t.Fatal("signing failure returned nil without error")
				}
				_, err = collector.Collect(boundedContext(t), collectionRequest())
			}
			check(t, errors.Is(err, ErrAuth), "unavailable signing identity did not produce an authentication error")
			check(t, calls.Load() == 0, "native call used unsigned, expired, or ambient signing identity")
		})
	}
}

func TestNativeDeliveryBoundaryRejectsMissingCredentialBeforePreviewOrSend(t *testing.T) {
	for _, profile := range []string{"jira-cloud-v3", "slack-workspace-bot", "teams-workflows-channel"} {
		t.Run(profile, func(t *testing.T) {
			if Production.OpenDelivery == nil {
				t.Fatal("production delivery binding missing")
			}
			base, client, calls := endpoint(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("missing delivery credential reached a native call")
				w.WriteHeader(401)
			})
			config := deliveryConfig(profile, base, client)
			config.Token = ""
			if profile == "teams-workflows-channel" {
				config.Endpoint += "/workflows/synthetic/triggers/manual/paths/invoke"
			}
			delivery, err := Production.OpenDelivery(boundedContext(t), config)
			if err == nil {
				if delivery == nil {
					t.Fatal("missing delivery credential returned nil without error")
				}
				if profile == "jira-cloud-v3" {
					_, err = delivery.Preview(boundedContext(t), actionFixture())
				} else {
					_, err = delivery.Send(boundedContext(t), actionFixture())
				}
			}
			check(t, errors.Is(err, ErrAuth), "missing token or signed Workflow URL did not fail authentication")
			check(t, calls.Load() == 0, "delivery attempted unsigned or ambient fallback")
		})
	}
}
