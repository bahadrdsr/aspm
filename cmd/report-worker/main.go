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
	config, err := service.Environment("reports")
	if err == nil {
		err = service.Run(ctx, "reports", config)
	}
	if err != nil {
		slog.Error("report worker stopped", "error", err)
		os.Exit(1)
	}
}
