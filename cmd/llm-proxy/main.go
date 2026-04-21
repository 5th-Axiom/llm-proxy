package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"llm-proxy/internal/admin"
	"llm-proxy/internal/config"
	"llm-proxy/internal/server"
)

func main() {
	var (
		configPath   string
		hashPassword bool
	)
	flag.StringVar(&configPath, "config", "config.yaml", "path to YAML config file")
	flag.BoolVar(&hashPassword, "hash-password", false,
		"read a password from stdin, print its PBKDF2 hash to stdout, and exit")
	flag.Parse()

	if hashPassword {
		if err := runHashPassword(os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	if cfg.Admin.PasswordHash == "" {
		logger.Warn("admin auth disabled — admin.password_hash is empty; UI and API are unauthenticated")
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

// runHashPassword reads a single line (the password) from in and writes its
// PBKDF2-SHA256 hash to out. Using stdin keeps the password out of shell
// history and `ps`. Trailing whitespace is trimmed — it almost never belongs
// to the password and confuses users.
func runHashPassword(in io.Reader, out, errOut io.Writer) error {
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(errOut, "password: ")
		}
	}
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return fmt.Errorf("password is empty")
	}
	hash, err := admin.HashPassword(password)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, hash)
	return nil
}
