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
	config, err := service.Environment("verification")
	if err != nil {
		return err
	}
	return service.Run(ctx, "verification", config)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("verification worker stopped", "error", err)
		os.Exit(1)
	}
}
