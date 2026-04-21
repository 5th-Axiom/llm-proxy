package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"llm-proxy/internal/config"
	"llm-proxy/internal/server"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "config.yaml", "path to YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	srv, err := server.New(context.Background(), cfg, configPath, logger)
	if err != nil {
		logger.Error("build server", "err", err)
		os.Exit(1)
	}

	logger.Info("starting llm proxy",
		"listen", cfg.Server.Listen,
		"metrics_listen", cfg.Server.MetricsListen,
	)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}
