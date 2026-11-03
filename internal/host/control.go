package host

import (
	"bytes"
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
		b.reason = "Host reboot is disabled. See the host upkeep setup in HOST.md."
	case hostname == "":
		b.reason = "Host reboot requires a known hostname."
	case os.Getenv("MONITOR_REBOOT_URL") != "":
		b.run = remoteReboot(os.Getenv("MONITOR_REBOOT_URL"))
	case runtime.GOOS != "linux" || inContainer():
		b.reason = "Host reboot requires Linux with systemd-logind; Docker installs need compose.upkeep.yml."
	default:
		b.run = logindReboot(env("SYSTEMD_SOCKET", "/run/dbus/system_bus_socket"))
	}
	return b
}

func (b *hostRebooter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestReboot, ok := hostActionRequest(w, r, b.hostname, "confirm the exact hostname to reboot")
	if !ok {
		return
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
		writeHostActionFailure(w, err)
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

func hostActionRequest(w http.ResponseWriter, r *http.Request, hostname, unconfirmed string) (post, ok bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false, false
	}
	if r.Method == http.MethodGet {
		return false, true
	}
	if r.Header.Get("X-Monitor") != "1" || !slices.Contains([]string{"", "same-origin", "none"}, r.Header.Get("Sec-Fetch-Site")) {
		writeRebootError(w, http.StatusForbidden, "cross-site requests are not allowed")
		return true, false
	}
	var body struct {
		Hostname string `json:"hostname"`
	}
	if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 4096), &body); err != nil || body.Hostname == "" || body.Hostname != hostname {
		writeRebootError(w, http.StatusBadRequest, unconfirmed)
		return true, false
	}
	return true, true
}

func writeHostActionFailure(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var failure *rebootError
	if errors.As(err, &failure) {
		code = failure.code
	}
	writeRebootError(w, code, err.Error())
}

func writeRebootError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.MarshalWrite(w, map[string]string{"error": message})
}

func remoteReboot(endpoint string) func(context.Context, bool) (rebootStatus, error) {
	return remoteHostAction(endpoint, "host reboot", func(code int, status rebootStatus) bool {
		return code == http.StatusAccepted && status.ScheduledAction == "reboot" && status.ScheduledAt > 0
	})
}

func remoteHostAction[T any](endpoint, name string, confirmed func(int, T) bool) func(context.Context, bool) (T, error) {
	client := &http.Client{Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return func(ctx context.Context, post bool) (T, error) {
		var status T
		method, body := http.MethodGet, ""
		if post {
			method, body = http.MethodPost, `{"hostname":"host"}`
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
		if err != nil {
			return status, fmt.Errorf("invalid %s URL", name)
		}
		req.Header.Set("X-Monitor", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return status, fmt.Errorf("%s service did not respond; check its status before retrying", name)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			var failure struct {
				Error string `json:"error"`
			}
			json.UnmarshalRead(io.LimitReader(resp.Body, 4096), &failure)
			if failure.Error == "" {
				failure.Error = name + " service: " + resp.Status
			}
			code := http.StatusBadGateway
			if slices.Contains([]int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict}, resp.StatusCode) {
				code = resp.StatusCode
			}
			return status, &rebootError{code, failure.Error}
		}
		if err := json.UnmarshalRead(io.LimitReader(resp.Body, 4096), &status); err != nil {
			return status, fmt.Errorf("invalid %s response; check its status before retrying", name)
		}
		if post && !confirmed(resp.StatusCode, status) {
			var zero T
			return zero, fmt.Errorf("%s was not confirmed; check its status before retrying", name)
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

const (
	updateUnit    = "monitor-host-updates.service"
	updateLogPath = "/var/log/monitor-host-updates.log"
	updateMarker  = "monitor-host-updates:"
)

var updateScript = strings.Join([]string{
	"exec >" + updateLogPath + " 2>&1",
	`echo "` + updateMarker + ` started $(date +%s)"`,
	"export DEBIAN_FRONTEND=noninteractive",
	"apt-get -o DPkg::Lock::Timeout=600 update && apt-get -y -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold --with-new-pkgs upgrade",
	"status=$?",
	`echo "` + updateMarker + ` finished $(date +%s) $status"`,
	"exit $status",
}, "\n")

type updateStatus struct {
	Available  bool     `json:"available"`
	Reason     string   `json:"reason,omitempty"`
	Running    bool     `json:"running,omitzero"`
	StartedAt  int64    `json:"startedAt,omitzero"`
	FinishedAt int64    `json:"finishedAt,omitzero"`
	ExitStatus *int     `json:"exitStatus,omitempty"`
	Output     []string `json:"output,omitempty"`
}

type hostUpdater struct {
	mu           sync.Mutex
	hostname     string
	reason       string
	logFile      string
	run          func(context.Context, bool) (updateStatus, error)
	finished     func()
	lastFinished int64
}

func configuredUpdater(hostname, root string, finished func()) *hostUpdater {
	u := &hostUpdater{hostname: hostname, logFile: filepath.Join(root, updateLogPath), finished: finished}
	enabled, _ := strconv.ParseBool(os.Getenv("MONITOR_UPDATES_ENABLED"))
	_, aptErr := os.Stat(filepath.Join(root, "usr/bin/apt-get"))
	switch {
	case !enabled:
		u.reason = "Installing updates is disabled. See the host upkeep setup in HOST.md."
	case hostname == "":
		u.reason = "Installing updates requires a known hostname."
	case aptErr != nil:
		u.reason = "Installing updates requires a Debian or Ubuntu host with apt."
	case os.Getenv("MONITOR_UPDATES_URL") != "":
		u.run = remoteHostAction(os.Getenv("MONITOR_UPDATES_URL"), "host updates", func(code int, status updateStatus) bool {
			return code == http.StatusAccepted && status.Running
		})
	case runtime.GOOS != "linux" || inContainer():
		u.reason = "Installing updates requires Linux with systemd; Docker installs need compose.upkeep.yml."
	default:
		u.run = systemdUpdates(env("SYSTEMD_SOCKET", "/run/dbus/system_bus_socket"))
	}
	return u
}

func (u *hostUpdater) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	install, ok := hostActionRequest(w, r, u.hostname, "confirm the exact hostname to install updates")
	if !ok {
		return
	}
	if u.reason != "" {
		if install {
			writeRebootError(w, http.StatusForbidden, u.reason)
		} else {
			writeJSON(w, updateStatus{Reason: u.reason})
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	status, err := u.run(ctx, install)
	if err != nil {
		writeHostActionFailure(w, err)
		return
	}
	if u.logFile != "" {
		readUpdateLog(u.logFile, &status)
	}
	if !status.Running && status.FinishedAt != u.lastFinished {
		u.lastFinished = status.FinishedAt
		if u.finished != nil {
			go u.finished()
		}
	}
	if install {
		slog.Warn("host updates requested", "hostname", u.hostname, "remote", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
	}
	writeJSON(w, status)
}

func readUpdateLog(path string, status *updateStatus) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	const limit = 64 << 10
	if st, err := f.Stat(); err == nil && st.Size() > limit {
		f.Seek(-limit, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))), "\n"), "\n")
	output := lines[:0]
	for _, line := range lines {
		if marker, ok := strings.CutPrefix(line, updateMarker+" "); ok {
			fields := strings.Fields(marker)
			switch {
			case len(fields) == 2 && fields[0] == "started":
				status.StartedAt, _ = strconv.ParseInt(fields[1], 10, 64)
				status.FinishedAt, status.ExitStatus = 0, nil
			case len(fields) == 3 && fields[0] == "finished":
				status.FinishedAt, _ = strconv.ParseInt(fields[1], 10, 64)
				if code, err := strconv.Atoi(fields[2]); err == nil {
					status.ExitStatus = &code
				}
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			output = append(output, line)
		}
	}
	status.Output = output[max(0, len(output)-200):]
	if status.Running {
		status.FinishedAt, status.ExitStatus = 0, nil
	}
}

func systemdUpdates(socket string) func(context.Context, bool) (updateStatus, error) {
	return func(ctx context.Context, install bool) (updateStatus, error) {
		conn, err := dbus.Connect("unix:path="+socket, dbus.WithContext(ctx))
		if err != nil {
			return updateStatus{}, fmt.Errorf("connect to host system bus: %w", err)
		}
		defer conn.Close()
		return startSystemdUpdates(ctx, conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1"), install)
	}
}

type unitProperty struct {
	Name  string
	Value dbus.Variant
}

type unitExec struct {
	Path      string
	Args      []string
	Uncleaned bool
}

func startSystemdUpdates(ctx context.Context, object logindCaller, install bool) (updateStatus, error) {
	const manager = "org.freedesktop.systemd1.Manager"
	var units []struct {
		Name, Description, LoadState, ActiveState, SubState, Following string
		Path                                                           dbus.ObjectPath
		JobID                                                          uint32
		JobType                                                        string
		JobPath                                                        dbus.ObjectPath
	}
	if err := object.CallWithContext(ctx, manager+".ListUnitsByNames", 0, []string{updateUnit}).Store(&units); err != nil {
		return updateStatus{}, fmt.Errorf("read host update status: %w", err)
	}
	running := len(units) > 0 && slices.Contains([]string{"active", "activating", "deactivating", "reloading"}, units[0].ActiveState)
	if !install {
		return updateStatus{Available: true, Running: running}, nil
	}
	if running {
		return updateStatus{}, &rebootError{http.StatusConflict, "updates are already being installed"}
	}
	properties := []unitProperty{
		{"Description", dbus.MakeVariant("Install host updates requested from Monitor")},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
		{"ExecStart", dbus.MakeVariant([]unitExec{{"/bin/sh", []string{"/bin/sh", "-c", updateScript}, false}})},
	}
	var job dbus.ObjectPath
	err := object.CallWithContext(ctx, manager+".StartTransientUnit", 0, updateUnit, "fail", properties, []struct {
		Name       string
		Properties []unitProperty
	}{}).Store(&job)
	if err != nil {
		return updateStatus{}, fmt.Errorf("start host updates: %w", err)
	}
	return updateStatus{Available: true, Running: true}, nil
}
