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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, err := service.Environment("retention")
	if err == nil {
		err = service.Run(ctx, "retention", config)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("retention worker stopped", "error", err)
		os.Exit(1)
	}
}
