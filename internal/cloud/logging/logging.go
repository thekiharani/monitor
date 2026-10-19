package logging

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

func ErrorAttrs(err error, attrs ...any) []any {
	result := make([]any, 0, len(attrs)+4)
	result = append(result, attrs...)
	if err != nil {
		result = append(result, "error", err, "error_type", fmt.Sprintf("%T", err))
	}
	return result
}

func Error(logger *slog.Logger, message string, err error, attrs ...any) {
	logger.Error(message, ErrorAttrs(err, attrs...)...)
}

func Warn(logger *slog.Logger, message string, err error, attrs ...any) {
	logger.Warn(message, ErrorAttrs(err, attrs...)...)
}

func RequestAttrs(r *http.Request) []any {
	return []any{
		"request_id", httpx.RequestID(r),
		"method", r.Method,
		"host", r.Host,
		"path", r.URL.Path,
		"user_agent", r.UserAgent(),
	}
}
