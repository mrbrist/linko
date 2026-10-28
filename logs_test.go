package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_requestLogger(t *testing.T) {
	logBuffer := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == "duration" {
				return slog.Duration("duration", time.Second)
			}
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			return a
		},
	}))

	requestLoggerMiddleware := requestLogger(logger)
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	loggedHandler := requestLoggerMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "http://lin.ko/api/stats", nil)
	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, req)

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.x request_id="" duration=1s request_body_bytes=0 response_status=200 response_body_bytes=0` + "\n"
	const expectedStatusCode = http.StatusOK

	// replace the .Skip() call with two checks to verify the log string and status code here
	// If either doesn't match, use t.Errorf to report the failure with a helpful message.
	// t.Skip()
	if logBuffer.String() != expectedLogString {
		t.Errorf(
			"Expected log string:\n%q\nGot:\n%q",
			expectedLogString,
			logBuffer.String(),
		)
	}

	if rr.Code != expectedStatusCode {
		t.Errorf(
			"Expected status code %d, got %d",
			expectedStatusCode,
			rr.Code,
		)
	}
}

func Test_redactIP(t *testing.T) {
	tests := map[string]string{
		"192.168.1.42:12345": "192.168.1.x",
		"192.168.1.42":       "192.168.1.x",
		"[::1]:59775":        "[::1]:59775",
		"::1":                "::1",
		"[::ffff:1.2.3.4]:1": "[::ffff:1.2.3.4]:1",
		"not an address":     "not an address",
	}
	for in, want := range tests {
		if got := redactIP(in); got != want {
			t.Errorf("redactIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func Test_replaceAttr_redaction(t *testing.T) {
	for _, key := range []string{"password", "User", "apikey", "secret"} {
		got := replaceAttr(nil, slog.String(key, "frodo"))
		if got.Value.String() != "[REDACTED]" {
			t.Errorf("key %q: got %q, want [REDACTED]", key, got.Value)
		}
	}

	tests := map[string]string{
		"https://frodo:hunter2@example.com/a?b=c": "https://frodo:[REDACTED]@example.com/a?b=c",
		"https://frodo@example.com":               "https://frodo@example.com",
		"https://example.com":                     "https://example.com",
		"just text":                               "just text",
	}
	for in, want := range tests {
		got := replaceAttr(nil, slog.String("url", in))
		if got.Value.String() != want {
			t.Errorf("url %q: got %q, want %q", in, got.Value, want)
		}
	}
}
