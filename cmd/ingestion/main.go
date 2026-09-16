package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bahadrdsr/aspm/internal/service"
)

func main() {
	check := flag.Bool("check", false, "Submit a synthetic byte-integrity job and wait for an independent worker")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, err := service.Environment("ingestion")
	if err == nil && *check {
		var report service.CheckReport
		report, err = service.Check(ctx, config)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(report)
		}
	} else if err == nil {
		err = service.Run(ctx, "ingestion", config)
	}
	if err != nil {
		slog.Error("ingestion service stopped", "error", err)
		os.Exit(1)
	}
}
