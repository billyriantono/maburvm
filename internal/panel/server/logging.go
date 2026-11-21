package server

import (
	"context"
	"log/slog"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// setupLogging makes every panel log line one JSON object on stderr. slog's
// default also carries the standard log package, so library output that uses
// it lands in the same format.
func setupLogging() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
}

// requestLogger logs each request through slog. The path is logged without
// its query string: console tokens travel there.
func requestLogger() echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		HandleError: true, LogLatency: true, LogRemoteIP: true, LogMethod: true, LogURIPath: true,
		LogRoutePath: true, LogStatus: true, LogError: true, LogResponseSize: true, LogUserAgent: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			level := slog.LevelInfo
			if v.Status >= 500 {
				level = slog.LevelError
			}
			attrs := []slog.Attr{
				slog.String("method", v.Method), slog.String("path", v.URIPath), slog.String("route", v.RoutePath),
				slog.Int("status", v.Status), slog.Int64("latency_ms", v.Latency.Milliseconds()),
				slog.String("remote_ip", v.RemoteIP), slog.Int64("bytes_out", v.ResponseSize), slog.String("user_agent", v.UserAgent),
			}
			if v.Error != nil {
				attrs = append(attrs, slog.String("error", v.Error.Error()))
			}
			slog.LogAttrs(context.Background(), level, "request", attrs...)
			return nil
		},
	})
}
