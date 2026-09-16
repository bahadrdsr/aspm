package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/bahadrdsr/aspm/internal/devhost"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8080", "Loopback listen address; this development host has no authentication")
	directory := flag.String("assets", filepath.Join("web", "dist"), "Built frontend asset directory")
	flag.Parse()
	if err := run(*address, *directory); err != nil {
		slog.Error("development host stopped", "error", err)
		os.Exit(1)
	}
}

func run(address, directory string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid loopback address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen must use a literal loopback IP; authentication is not implemented")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open built assets (run the frontend build first): %w", err)
	}
	defer root.Close()
	handler, err := devhost.New(root.FS())
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	slog.Info("M01 development host listening", "address", listener.Addr().String(), "data_apis", "unavailable", "authentication", "not-implemented")
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
