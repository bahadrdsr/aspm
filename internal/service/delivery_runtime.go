package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func deliveryEnvironment(config *Config) error {
	var err error
	config.JiraAPIOrigins, err = originEnvironment("ASPM_JIRA_API_ORIGINS")
	if err != nil {
		return err
	}
	if _, err := jiraOriginAddresses(config.JiraAPIOrigins); err != nil {
		return err
	}
	config.TeamsWorkflowOrigins, err = originEnvironment("ASPM_TEAMS_WORKFLOW_ORIGINS")
	if err != nil {
		return err
	}
	if _, err := teamsOriginAddresses(config.TeamsWorkflowOrigins); err != nil {
		return err
	}
	key, err := integrationEncryptionKey(os.Getenv("ASPM_INTEGRATION_ENCRYPTION_KEY"), true)
	if err != nil {
		return err
	}
	config.IntegrationEncryptionKey = key
	config.DeliveryLeaseDuration = 15 * time.Second
	if value := os.Getenv("ASPM_DELIVERY_LEASE_DURATION"); value != "" {
		config.DeliveryLeaseDuration, err = time.ParseDuration(value)
		if err != nil {
			return errors.New("delivery lease duration is invalid")
		}
	}
	config.SlackEndpoint, err = app.ValidateDeliveryGateway(env("ASPM_SLACK_ENDPOINT", "https://slack.com"))
	if err != nil {
		return err
	}
	config.DeliveryCAFile = os.Getenv("ASPM_DELIVERY_CA_FILE")
	config.DeliveryClient, err = newGatewayProviderClient(config.SlackEndpoint, config.DeliveryCAFile,
		config.JiraAPIOrigins, config.TeamsWorkflowOrigins, config.WebhookOrigins)
	return err
}

func originEnvironment(name string) ([]string, error) {
	value := os.Getenv(name)
	if value == "" {
		return nil, nil
	}
	var origins []string
	if err := json.Unmarshal([]byte(value), &origins); err != nil || origins == nil {
		return nil, errors.New(name + " must be a JSON array of canonical HTTPS origins")
	}
	return origins, nil
}

func deliveryWorkerConfig(config Config) app.DeliveryWorkerConfig {
	return app.DeliveryWorkerConfig{
		Database: databaseConfig(config.Jobs), EncryptionKey: config.IntegrationEncryptionKey,
		WorkerID: config.WorkerID, LeaseDuration: config.DeliveryLeaseDuration,
		PublicOrigin: config.PublicOrigin, WebhookOrigins: config.WebhookOrigins,
		SlackEndpoint: config.SlackEndpoint, Client: config.DeliveryClient,
	}
}

func validateDeliveryConfig(config Config) error {
	if _, err := jiraOriginAddresses(config.JiraAPIOrigins); err != nil {
		return err
	}
	if _, err := teamsOriginAddresses(config.TeamsWorkflowOrigins); err != nil {
		return err
	}
	if _, err := webhookOriginAddresses(config.WebhookOrigins); err != nil {
		return err
	}
	if err := app.ValidateDeliveryWorkerConfig(deliveryWorkerConfig(config)); err != nil {
		return err
	}
	endpoint, err := app.ValidateDeliveryGateway(config.SlackEndpoint)
	if err != nil {
		return err
	}
	return validateDeliveryClient(config.DeliveryClient, endpoint)
}

func runDelivery(ctx context.Context, worker *app.DeliveryWorker) error {
	return runQueuedWork(ctx, "delivery", worker.ProcessNext)
}

func runQueuedWork(ctx context.Context, role string, process func(context.Context) (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := process(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New(role + " processing stopped after an infrastructure failure")
		}
		if processed {
			continue
		}
		idle := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			idle.Stop()
			return ctx.Err()
		case <-idle.C:
		}
	}
}
