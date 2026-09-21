package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bahadrdsr/aspm/internal/service"
)

func run(ctx context.Context) error {
	config, err := service.Environment("assessment")
	if err != nil {
		return err
	}
	defer config.AssessmentClient.CloseIdleConnections()
	defer clear(config.IntegrationEncryptionKey)
	return service.Run(ctx, "assessment", config)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("assessment worker stopped", "error", err)
		os.Exit(1)
	}
}
