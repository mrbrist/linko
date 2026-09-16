package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

type closeFunc func() error

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	logFile := os.Getenv("LINKO_LOG_FILE")

	logger, closeLogger, err := initializeLogger(logFile)
	if err != nil {
		log.Printf("failed to initialize logger: %v", err)
		os.Exit(1)
	}
	defer func() {
		if err := closeLogger(); err != nil {
			log.Printf("failed to close logger: %v", err)
		}
	}()

	status := run(ctx, cancel, *httpPort, *dataDir, logger)
	logger.Printf("Linko is running on http://localhost:%d", *httpPort)

	cancel()
	logger.Println("Linko is shutting down")
	os.Exit(status)
}

func run(
	ctx context.Context,
	cancel context.CancelFunc,
	httpPort int,
	dataDir string,
	logger *log.Logger,
) int {
	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Printf("failed to create store: %v", err)
		return 1
	}

	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error

	go func() {
		serverErr = s.start()
	}()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		logger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}

func initializeLogger(logFile string) (*log.Logger, closeFunc, error) {
	if logFile == "" {
		return log.New(os.Stderr, "", log.LstdFlags), func() error {
			return nil
		}, nil
	}

	f, err := os.OpenFile(
		logFile,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return nil, nil, err
	}

	writer := io.MultiWriter(os.Stderr, f)
	bufferedWriter := bufio.NewWriterSize(writer, 8192)

	closeLogger := func() error {
		if err := bufferedWriter.Flush(); err != nil {
			f.Close()
			return err
		}

		return f.Close()
	}

	return log.New(bufferedWriter, "", log.LstdFlags), closeLogger, nil
}
