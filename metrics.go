package main

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var httpRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests by method, path and status.",
	},
	[]string{"method", "path", "status"},
)

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spyWriter := &spyResponseWriter{ResponseWriter: w}

		next.ServeHTTP(spyWriter, r)

		status := spyWriter.statusCode
		if status == 0 {
			status = http.StatusOK
		}
		httpRequestsTotal.WithLabelValues(
			r.Method,
			r.URL.Path,
			strconv.Itoa(status),
		).Inc()
	})
}
