package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/logging"
)

type responseWriter struct {
	http.ResponseWriter
	status              int
	accountID, apiKeyID string
}

func (w *responseWriter) WriteHeader(s int) {
	if w.status != 0 {
		return
	}
	w.status = s
	w.ResponseWriter.WriteHeader(s)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func wrote(w http.ResponseWriter) bool {
	if x, ok := w.(*responseWriter); ok {
		return x.status != 0
	}
	return false
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-ID")
		if !httpx.SafeRequestID(id) {
			id = httpx.NewRequestID()
		}
		r = r.WithContext(context.WithValue(r.Context(), httpx.RequestIDKey, id))
		rw := &responseWriter{ResponseWriter: w}
		rw.Header().Set("X-Request-ID", id)
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "same-origin")
		rw.Header().Set("X-Frame-Options", "DENY")
		rw.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		rw.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		rw.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if a.cfg.HSTSMaxAge > 0 {
			rw.Header().Set("Strict-Transport-Security", a.cfg.HSTSHeader)
		}
		rw.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"font-src 'self'; "+
				"img-src 'self' data: blob:; "+
				"script-src 'self' '"+themeScriptHash+"' https://js.paystack.co https://*.paystack.co https://*.paystack.com; "+
				"frame-src 'self' https://*.paystack.co https://*.paystack.com; "+
				"connect-src 'self' https://*.paystack.co https://*.paystack.com; "+
				"form-action 'self'")
		defer func() {
			if x := recover(); x != nil {
				panicAttrs := append(logging.RequestAttrs(r), "panic_type", fmt.Sprintf("%T", x))
				a.logError("panic recovered", "http", "recover_panic", fmt.Errorf("%v", x), panicAttrs...)
				if !wrote(rw) {
					httpx.WriteError(rw, r, httpx.NewError("internal_error"))
				}
			}
			status := rw.status
			if status == 0 {
				status = http.StatusOK
			}
			a.metrics.recordHTTP(status)
			attrs := []any{
				"request_id", id,
				"method", r.Method,
				"host", r.Host,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_ip", a.clientIPString(r),
			}
			if rw.accountID != "" {
				attrs = append(attrs, "account_id", rw.accountID)
			}
			if rw.apiKeyID != "" {
				attrs = append(attrs, "api_key_id", rw.apiKeyID)
			}
			attrs = append(attrs, "component", "http", "operation", "request", "outcome", requestOutcome(status), "user_agent", r.UserAgent())
			if status >= http.StatusInternalServerError {
				attrs = append(attrs, "error_type", "http_status", "error", fmt.Sprintf("HTTP %d response", status))
				a.log.Error("request", attrs...)
			} else if status >= http.StatusBadRequest {
				a.log.Warn("request", attrs...)
			} else {
				a.log.Info("request", attrs...)
			}
		}()
		if a.cors(rw, r) {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func requestOutcome(status int) string {
	switch {
	case status >= http.StatusInternalServerError:
		return "failure"
	case status >= http.StatusBadRequest:
		return "rejected"
	default:
		return "success"
	}
}

func (a *App) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	w.Header().Add("Vary", "Origin")
	if !slices.Contains(a.cfg.CORSAllowedOrigins, origin) {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token, X-Request-ID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After")
		w.Header().Set("Access-Control-Max-Age", "600")
		return true
	}
	return false
}

func (a *App) requireHost(w http.ResponseWriter, r *http.Request, origins ...string) bool {
	if !httpx.HostMatches(r, origins...) {
		w.WriteHeader(http.StatusMisdirectedRequest)
		return false
	}
	return true
}

func setRequestPrincipal(w http.ResponseWriter, p Principal) {
	if rw, ok := w.(*responseWriter); ok {
		rw.accountID = p.AccountID.String()
		if p.APIKeyID != nil {
			rw.apiKeyID = p.APIKeyID.String()
		}
	}
}

func isDashboardAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/dashboard/v1/")
}

func isJSONAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}
