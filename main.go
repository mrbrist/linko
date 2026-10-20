package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

type closeFunc func() error

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	logger, closeLogs, err := newLogger()
	if err != nil {
		slog.Error("failed to initialize logger", "err", err)
		cancel()
		os.Exit(1)
	}

	logger.Debug("Linko is starting",
		"port", *httpPort,
		"data", *dataDir,
	)

	status := run(ctx, cancel, *httpPort, *dataDir, logger)

	logger.Debug("Linko is shutting down")
	cancel()

	if err := closeLogs(); err != nil {
		logger.Error("failed to close log file", "err", err)
		status = 1
	}

	os.Exit(status)
}

func newLogger() (*slog.Logger, closeFunc, error) {
	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	logFilePath := os.Getenv("LINKO_LOG_FILE")
	if logFilePath == "" {
		return slog.New(stderrHandler), func() error {
			return nil
		}, nil
	}

	file, err := os.OpenFile(
		logFilePath,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0644,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}

	fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	logger := slog.New(slog.NewMultiHandler(
		stderrHandler,
		fileHandler,
	))

	return logger, func() error {
		return file.Close()
	}, nil
}

func run(
	ctx context.Context,
	cancel context.CancelFunc,
	httpPort int,
	dataDir string,
	logger *slog.Logger,
) int {
	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error("failed to create store", "err", err)
		return 1
	}

	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error

	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer shutdownCancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server", "err", err)
		return 1
	}

	if serverErr != nil {
		logger.Error("server error", "err", serverErr)
		return 1
	}

	return 0
}
