package host

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

type rebootStatus struct {
	Available       bool   `json:"available"`
	Reason          string `json:"reason,omitempty"`
	ScheduledAt     int64  `json:"scheduledAt,omitzero"`
	ScheduledAction string `json:"scheduledAction,omitempty"`
	BootID          string `json:"bootId,omitempty"`
}

type rebootError struct {
	code int
	text string
}

func (e *rebootError) Error() string { return e.text }

type hostRebooter struct {
	mu       sync.Mutex
	hostname string
	reason   string
	bootID   func() string
	run      func(context.Context, bool) (rebootStatus, error)
}

func configuredRebooter(hostname, proc string) *hostRebooter {
	b := &hostRebooter{hostname: hostname, bootID: func() string {
		return firstLine(filepath.Join(proc, "sys/kernel/random/boot_id"))
	}}
	enabled, _ := strconv.ParseBool(os.Getenv("MONITOR_REBOOT_ENABLED"))
	switch {
	case !enabled:
		b.reason = "Host reboot is disabled. See the host reboot setup in HOST.md."
	case hostname == "":
		b.reason = "Host reboot requires a known hostname."
	case os.Getenv("MONITOR_REBOOT_URL") != "":
		b.run = remoteReboot(os.Getenv("MONITOR_REBOOT_URL"))
	case runtime.GOOS != "linux" || inContainer():
		b.reason = "Host reboot requires Linux with systemd-logind; Docker installs need compose.reboot.yml."
	default:
		b.run = logindReboot(env("SYSTEMD_SOCKET", "/run/dbus/system_bus_socket"))
	}
	return b
}

func (b *hostRebooter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestReboot := r.Method == http.MethodPost
	if requestReboot {
		if r.Header.Get("X-Monitor") != "1" || !slices.Contains([]string{"", "same-origin", "none"}, r.Header.Get("Sec-Fetch-Site")) {
			writeRebootError(w, http.StatusForbidden, "cross-site requests are not allowed")
			return
		}
		var body struct {
			Hostname string `json:"hostname"`
		}
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 4096), &body); err != nil || body.Hostname == "" || body.Hostname != b.hostname {
			writeRebootError(w, http.StatusBadRequest, "confirm the exact hostname to reboot")
			return
		}
	}
	if b.reason != "" {
		if requestReboot {
			writeRebootError(w, http.StatusForbidden, b.reason)
		} else {
			writeJSON(w, rebootStatus{Reason: b.reason})
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b.mu.Lock()
	status, err := b.run(ctx, requestReboot)
	b.mu.Unlock()
	if err != nil {
		code := http.StatusBadGateway
		var failure *rebootError
		if errors.As(err, &failure) {
			code = failure.code
		}
		writeRebootError(w, code, err.Error())
		return
	}
	if b.bootID != nil {
		status.BootID = b.bootID()
	}
	if requestReboot {
		slog.Warn("host reboot requested", "hostname", b.hostname, "remote", r.RemoteAddr, "scheduledAt", status.ScheduledAt)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
	}
	writeJSON(w, status)
}

func writeRebootError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.MarshalWrite(w, map[string]string{"error": message})
}

func remoteReboot(endpoint string) func(context.Context, bool) (rebootStatus, error) {
	client := &http.Client{Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return func(ctx context.Context, requestReboot bool) (rebootStatus, error) {
		method, body := http.MethodGet, ""
		if requestReboot {
			method, body = http.MethodPost, `{"hostname":"host"}`
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
		if err != nil {
			return rebootStatus{}, fmt.Errorf("invalid host reboot URL")
		}
		req.Header.Set("X-Monitor", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return rebootStatus{}, fmt.Errorf("host reboot service did not respond; check its status before retrying")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			var failure struct {
				Error string `json:"error"`
			}
			json.UnmarshalRead(io.LimitReader(resp.Body, 4096), &failure)
			if failure.Error == "" {
				failure.Error = "host reboot service: " + resp.Status
			}
			code := http.StatusBadGateway
			if slices.Contains([]int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict}, resp.StatusCode) {
				code = resp.StatusCode
			}
			return rebootStatus{}, &rebootError{code, failure.Error}
		}
		var status rebootStatus
		if err := json.UnmarshalRead(io.LimitReader(resp.Body, 4096), &status); err != nil {
			return status, fmt.Errorf("invalid host reboot response; check its status before retrying")
		}
		if requestReboot && (resp.StatusCode != http.StatusAccepted || status.ScheduledAction != "reboot" || status.ScheduledAt <= 0) {
			return rebootStatus{}, fmt.Errorf("host reboot was not confirmed; check its status before retrying")
		}
		return status, nil
	}
}

func logindReboot(socket string) func(context.Context, bool) (rebootStatus, error) {
	return func(ctx context.Context, requestReboot bool) (rebootStatus, error) {
		conn, err := dbus.Connect("unix:path="+socket, dbus.WithContext(ctx))
		if err != nil {
			return rebootStatus{}, fmt.Errorf("connect to host system bus: %w", err)
		}
		defer conn.Close()
		object := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
		return scheduleLogindReboot(ctx, object, requestReboot)
	}
}

type logindCaller interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

func scheduleLogindReboot(ctx context.Context, object logindCaller, requestReboot bool) (rebootStatus, error) {
	const manager = "org.freedesktop.login1.Manager"
	var scheduled dbus.Variant
	err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, manager, "ScheduledShutdown").Store(&scheduled)
	if err != nil {
		return rebootStatus{}, fmt.Errorf("read host shutdown status: %w", err)
	}
	var shutdown struct {
		Action string
		At     uint64
	}
	if err := scheduled.Store(&shutdown); err != nil {
		return rebootStatus{}, fmt.Errorf("read scheduled shutdown: %w", err)
	}
	if shutdown.Action != "" {
		if requestReboot {
			return rebootStatus{}, &rebootError{http.StatusConflict, "the host already has a shutdown scheduled"}
		}
		return rebootStatus{ScheduledAction: shutdown.Action, ScheduledAt: int64(shutdown.At / 1000)}, nil
	}
	var permission string
	if err := object.CallWithContext(ctx, manager+".CanReboot", 0).Store(&permission); err != nil {
		return rebootStatus{}, fmt.Errorf("check host reboot permission: %w", err)
	}
	if permission != "yes" {
		if requestReboot {
			return rebootStatus{}, &rebootError{http.StatusForbidden, "host reboot permission is not granted"}
		}
		return rebootStatus{Reason: "Host reboot permission is not granted to the monitor service."}, nil
	}
	if !requestReboot {
		return rebootStatus{Available: true}, nil
	}
	at := time.Now().Add(5 * time.Second)
	if err := object.CallWithContext(ctx, manager+".ScheduleShutdown", 0, "reboot", uint64(at.UnixMicro())).Err; err != nil {
		return rebootStatus{}, fmt.Errorf("schedule host reboot: %w", err)
	}
	return rebootStatus{ScheduledAction: "reboot", ScheduledAt: at.UnixMilli()}, nil
}
