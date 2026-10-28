package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"boot.dev/linko/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: requestID(requestLogger(logger)(metricsMiddleware(mux))),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	if w.statusCode == 0 {
		w.statusCode = statusCode
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

// Unwrap lets http.ResponseController reach the underlying ResponseWriter.
func (w *spyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// httpError records err in the request's LogContext so requestLogger logs it
// once, then sends the response. For 401, 403 and 500 only the generic status
// text is sent so internal details aren't exposed to clients.
func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}
	msg := err.Error()
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError:
		msg = http.StatusText(status)
	}
	http.Error(w, msg, status)
}

const requestIDHeader = "X-Request-ID"

// requestID makes sure every response carries an X-Request-ID, reusing the
// inbound one when present.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(requestIDHeader)
		if reqID == "" {
			reqID = rand.Text()
		}
		w.Header().Set(requestIDHeader, reqID)
		next.ServeHTTP(w, r)
	})
}

// redactIP replaces the last octet of an IPv4 address (with or without a port)
// with "x". Anything else is returned unchanged.
func redactIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || strings.Contains(host, ":") {
		return addr
	}
	octets := strings.Split(host, ".")
	octets[3] = "x"
	return strings.Join(octets, ".")
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader
			logCtx := &LogContext{}
			r = r.WithContext(context.WithValue(r.Context(), logContextKey, logCtx))
			spyWriter := &spyResponseWriter{ResponseWriter: w}

			next.ServeHTTP(spyWriter, r)

			status := spyWriter.statusCode
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"client_ip", redactIP(r.RemoteAddr),
				"request_id", w.Header().Get(requestIDHeader),
				"duration", time.Since(start),
				"request_body_bytes", spyReader.bytesRead,
				"response_status", status,
				"response_body_bytes", spyWriter.bytesWritten,
			}
			if logCtx.Username != "" {
				attrs = append(attrs, "user", logCtx.Username)
			}
			if logCtx.Error != nil {
				attrs = append(attrs, "error", logCtx.Error)
			}
			logger.Info("Served request", attrs...)
		})
	}
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
