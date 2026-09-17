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
	config, err := service.Environment("collection")
	if err != nil {
		return err
	}
	defer config.CollectionClient.CloseIdleConnections()
	return service.Run(ctx, "collection", config)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("collection worker stopped", "error", err)
		os.Exit(1)
	}
}
