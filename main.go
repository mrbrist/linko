package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
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

	hostname, err := os.Hostname()
	if err != nil {
		logger.Warn("failed to get hostname", "error", err)
		hostname = "unknown"
	}

	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)

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
	useColor := isatty.IsTerminal(os.Stderr.Fd()) ||
		isatty.IsCygwinTerminal(os.Stderr.Fd())
	stderrHandler := tint.NewTextHandler(os.Stderr, &tint.Options{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
		NoColor:     !useColor,
	})

	logFilePath := os.Getenv("LINKO_LOG_FILE")
	if logFilePath == "" {
		return slog.New(stderrHandler), func() error {
			return nil
		}, nil
	}

	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    1,
		MaxAge:     28,
		MaxBackups: 10,
		LocalTime:  false,
		Compress:   true,
	}

	fileHandler := slog.NewJSONHandler(fileLogger, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
	})

	logger := slog.New(slog.NewMultiHandler(
		stderrHandler,
		fileHandler,
	))

	return logger, func() error {
		return fileLogger.Close()
	}, nil
}

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

const redacted = "[REDACTED]"

// sensitiveKeys are attribute keys whose values are never logged. Usernames
// are treated as sensitive here too.
var sensitiveKeys = []string{
	"password",
	"key",
	"apikey",
	"secret",
	"pin",
	"creditcardno",
	"user",
}

// redactURLPassword masks the password in a URL's userinfo, if s is a URL
// that has one. Anything else is returned unchanged.
func redactURLPassword(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return s
	}
	u.User = url.UserPassword(u.User.Username(), redacted)
	// url.UserPassword percent-encodes the brackets
	return strings.Replace(u.String(), url.QueryEscape(redacted), redacted, 1)
}

type multiError interface {
	error
	Unwrap() []error
}

// replaceAttr groups errors under "error", or under "errors" with numbered
// entries when the error wraps several.
func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if slices.Contains(sensitiveKeys, strings.ToLower(a.Key)) {
		return slog.String(a.Key, redacted)
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, redactURLPassword(a.Value.String()))
	}
	if a.Value.Kind() != slog.KindAny {
		return a
	}
	err, ok := a.Value.Any().(error)
	if !ok {
		return a
	}
	if multiErr, ok := errors.AsType[multiError](err); ok {
		var errAttrs []slog.Attr
		for i, e := range multiErr.Unwrap() {
			errAttrs = append(errAttrs, slog.GroupAttrs(
				fmt.Sprintf("error_%d", i+1),
				errorAttrs(e)...,
			))
		}
		return slog.GroupAttrs("errors", errAttrs...)
	}
	return slog.GroupAttrs("error", errorAttrs(err)...)
}

// errorAttrs builds the message, linkoerr attributes and, when present, the
// stack trace attributes for err.
func errorAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{
		{Key: "message", Value: slog.StringValue(err.Error())},
	}
	attrs = append(attrs, linkoerr.Attrs(err)...)
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		})
	}
	return attrs
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
