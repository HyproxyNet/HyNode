package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"hynode/internal/app"
	"hynode/internal/config"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: hynode run [-c /etc/hynode/config.yaml]")
		os.Exit(2)
	}
	flags := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := flags.String("c", "", "configuration file path")
	_ = flags.Parse(os.Args[2:])

	cfg, warnings, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}
	for _, w := range warnings {
		slog.Warn("configuration warning", "msg", w)
	}
	level := slog.LevelInfo
	if cfg.Runtime.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting hynode", "version", version, "commit", commit, "buildTime", buildTime)

	service, err := app.New(cfg, logger)
	if err != nil {
		logger.Error("initialize service", "error", err)
		os.Exit(1)
	}
	if err = service.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("run service", "error", err)
		os.Exit(1)
	}
}
