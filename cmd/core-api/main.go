package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bahadrdsr/aspm/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, err := service.Environment("core")
	if err == nil {
		err = service.Run(ctx, "core", config)
	}
	if err != nil {
		slog.Error("core service stopped", "error", err)
		os.Exit(1)
	}
}
