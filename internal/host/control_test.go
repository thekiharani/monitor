package host

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type fakeLogind struct {
	action     string
	at         uint64
	permission string
	failure    error
	schedules  int
}

func (f *fakeLogind) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	switch method {
	case "org.freedesktop.DBus.Properties.Get":
		return &dbus.Call{Body: []any{dbus.MakeVariant([]any{f.action, f.at})}}
	case "org.freedesktop.login1.Manager.CanReboot":
		return &dbus.Call{Body: []any{f.permission}}
	case "org.freedesktop.login1.Manager.ScheduleShutdown":
		f.schedules++
		if f.failure == nil {
			f.action, f.at = args[0].(string), args[1].(uint64)
		}
		return &dbus.Call{Err: f.failure}
	default:
		return &dbus.Call{Err: errors.New("unexpected method: " + method)}
	}
}

func rebootRequest(handler http.Handler, method, body, password, site, header string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/host/reboot", strings.NewReader(body))
	if password != "" {
		r.SetBasicAuth("monitor", password)
	}
	r.Header.Set("X-Monitor", header)
	r.Header.Set("Sec-Fetch-Site", site)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestHostRebootRequiresAuthenticationAndExactConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, body, password, site, header string
		want                               int
	}{
		{"unauthenticated", `{"hostname":"vm1"}`, "", "same-origin", "1", 401},
		{"wrong password", `{"hostname":"vm1"}`, "wrong", "same-origin", "1", 401},
		{"cross site", `{"hostname":"vm1"}`, "secret", "cross-site", "1", 403},
		{"same site", `{"hostname":"vm1"}`, "secret", "same-site", "1", 403},
		{"missing header", `{"hostname":"vm1"}`, "secret", "same-origin", "", 403},
		{"wrong hostname", `{"hostname":"other"}`, "secret", "same-origin", "1", 400},
		{"missing hostname", `{}`, "secret", "same-origin", "1", 400},
		{"invalid json", `{`, "secret", "same-origin", "1", 400},
		{"trailing json", `{"hostname":"vm1"}{}`, "secret", "same-origin", "1", 400},
		{"oversized", `{"hostname":"vm1","extra":"` + strings.Repeat("a", 5000) + `"}`, "secret", "same-origin", "1", 400},
		{"confirmed", `{"hostname":"vm1"}`, "secret", "same-origin", "1", 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := &hostRebooter{hostname: "vm1", run: func(context.Context, bool) (rebootStatus, error) {
				calls++
				return rebootStatus{ScheduledAction: "reboot", ScheduledAt: 123}, nil
			}}
			w := rebootRequest(secure(b, "secret"), http.MethodPost, tc.body, tc.password, tc.site, tc.header)
			if w.Code != tc.want || (calls > 0) != (tc.want == 202) {
				t.Fatalf("response=%d body=%s calls=%d", w.Code, w.Body, calls)
			}
		})
	}
}

func TestHostRebootDisabledUnlessExplicitlyEnabled(t *testing.T) {
	for _, enabled := range []string{"", "false", "no"} {
		t.Setenv("MONITOR_REBOOT_ENABLED", enabled)
		b := configuredRebooter("vm1", t.TempDir())
		if b.reason == "" || b.run != nil {
			t.Fatalf("enabled without explicit opt-in: %+v", b)
		}
		w := rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
		if w.Code != 403 {
			t.Fatalf("disabled reboot returned %d", w.Code)
		}
		w = rebootRequest(b, http.MethodGet, "", "", "", "")
		var status rebootStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.Available || status.Reason == "" {
			t.Fatalf("disabled status: %s, err=%v", w.Body, err)
		}
	}
}

func TestHostRebootEnabledWithoutDashboardPassword(t *testing.T) {
	t.Setenv("MONITOR_REBOOT_ENABLED", "true")
	t.Setenv("MONITOR_PASSWORD", "")
	t.Setenv("MONITOR_REBOOT_URL", "http://docker-proxy:2375/host/reboot")
	if b := configuredRebooter("vm1", t.TempDir()); b.reason != "" || b.run == nil {
		t.Fatalf("reboot stayed disabled without a password: %+v", b)
	}
}

func TestHostRebootSchedulesGracefullyAndRejectsConcurrentDuplicates(t *testing.T) {
	f := &fakeLogind{permission: "yes"}
	b := &hostRebooter{hostname: "vm1", bootID: func() string { return "boot-before" }, run: func(ctx context.Context, requested bool) (rebootStatus, error) {
		return scheduleLogindReboot(ctx, f, requested)
	}}
	start := time.Now()
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			w := rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
			codes <- w.Code
		})
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for code := range codes {
		if code == 202 {
			accepted++
		} else if code != 409 {
			t.Fatalf("unexpected response %d", code)
		}
	}
	if accepted != 1 || f.schedules != 1 || f.action != "reboot" || f.at < uint64(start.Add(5*time.Second).UnixMicro()) || f.at > uint64(time.Now().Add(5*time.Second).UnixMicro()) {
		t.Fatalf("accepted=%d schedules=%d action=%s time=%d", accepted, f.schedules, f.action, f.at)
	}
	w := rebootRequest(b, http.MethodGet, "", "", "", "")
	var status rebootStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Available || status.BootID != "boot-before" || status.ScheduledAt != int64(f.at/1000) {
		t.Fatalf("status=%s error=%v", w.Body, err)
	}
}

func TestHostRebootPreservesExistingShutdownAndChecksPermissions(t *testing.T) {
	for _, action := range []string{"poweroff", "reboot"} {
		f := &fakeLogind{action: action, at: 123000}
		_, err := scheduleLogindReboot(context.Background(), f, true)
		var failure *rebootError
		if !errors.As(err, &failure) || failure.code != 409 || f.schedules != 0 {
			t.Fatalf("existing %s changed: err=%v schedules=%d", action, err, f.schedules)
		}
	}
	for _, permission := range []string{"no", "challenge", "na"} {
		f := &fakeLogind{permission: permission}
		status, err := scheduleLogindReboot(context.Background(), f, false)
		if err != nil || status.Available || status.Reason == "" {
			t.Fatalf("permission %s: status=%+v err=%v", permission, status, err)
		}
		_, err = scheduleLogindReboot(context.Background(), f, true)
		var failure *rebootError
		if !errors.As(err, &failure) || failure.code != 403 || f.schedules != 0 {
			t.Fatalf("permission %s: err=%v schedules=%d", permission, err, f.schedules)
		}
	}
}

func TestHostRebootProxyRoundTripAndSchedulingFailure(t *testing.T) {
	f := &fakeLogind{permission: "yes"}
	proxy := httptest.NewServer(&hostRebooter{hostname: "host", run: func(ctx context.Context, requested bool) (rebootStatus, error) {
		return scheduleLogindReboot(ctx, f, requested)
	}})
	defer proxy.Close()
	b := &hostRebooter{hostname: "vm1", run: remoteReboot(proxy.URL), bootID: func() string { return "host-boot" }}
	w := rebootRequest(b, http.MethodGet, "", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) || f.schedules != 0 {
		t.Fatalf("availability: %d %s", w.Code, w.Body)
	}
	f.failure = errors.New("host refused scheduling")
	w = rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "host refused scheduling") || f.action != "" {
		t.Fatalf("failure: %d %s", w.Code, w.Body)
	}
	f.failure = nil
	w = rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"bootId":"host-boot"`) {
		t.Fatalf("accepted: %d %s", w.Code, w.Body)
	}
	w = rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
	if w.Code != 409 {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
}

func TestHostRebootProxyDoesNotRetryOrFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"redirect", 307, ""},
		{"invalid response", 202, "not json"},
		{"unconfirmed", 200, `{"available":true}`},
		{"unconfirmed action", 202, `{"scheduledAction":"poweroff","scheduledAt":123}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/other")
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer proxy.Close()
			b := &hostRebooter{hostname: "vm1", run: remoteReboot(proxy.URL)}
			w := rebootRequest(b, http.MethodPost, `{"hostname":"vm1"}`, "", "same-origin", "1")
			if w.Code != 502 || calls.Load() != 1 {
				t.Fatalf("response=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
			}
		})
	}
}

type fakeSystemd struct {
	activeState string
	failure     error
	started     []string
	properties  []unitProperty
}

func (f *fakeSystemd) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	switch method {
	case "org.freedesktop.systemd1.Manager.ListUnitsByNames":
		return &dbus.Call{Body: []any{[][]any{{updateUnit, "", "loaded", f.activeState, "", "", dbus.ObjectPath("/unit"), uint32(0), "", dbus.ObjectPath("/")}}}}
	case "org.freedesktop.systemd1.Manager.StartTransientUnit":
		if f.failure != nil {
			return &dbus.Call{Err: f.failure}
		}
		f.started = append(f.started, args[0].(string))
		f.properties = args[2].([]unitProperty)
		f.activeState = "active"
		return &dbus.Call{Body: []any{dbus.ObjectPath("/job/1")}}
	default:
		return &dbus.Call{Err: errors.New("unexpected method: " + method)}
	}
}

func updateRequest(handler http.Handler, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/host/updates", strings.NewReader(body))
	r.Header.Set("X-Monitor", "1")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestHostUpdatesStartOneTransientUnitThatSurvivesTheMonitor(t *testing.T) {
	f := &fakeSystemd{activeState: "inactive"}
	status, err := startSystemdUpdates(context.Background(), f, false)
	if err != nil || !status.Available || status.Running || len(f.started) != 0 {
		t.Fatalf("idle status=%+v err=%v started=%v", status, err, f.started)
	}
	status, err = startSystemdUpdates(context.Background(), f, true)
	if err != nil || !status.Running || len(f.started) != 1 || f.started[0] != updateUnit {
		t.Fatalf("start status=%+v err=%v started=%v", status, err, f.started)
	}
	props := map[string]any{}
	for _, p := range f.properties {
		props[p.Name] = p.Value.Value()
	}
	exec := props["ExecStart"].([]unitExec)
	script := exec[0].Args[2]
	if props["CollectMode"] != "inactive-or-failed" || exec[0].Path != "/bin/sh" || !strings.Contains(script, "apt-get -o DPkg::Lock::Timeout=600 update &&") || !strings.Contains(script, "--with-new-pkgs upgrade") || !strings.Contains(script, "--force-confold") || !strings.Contains(script, ">"+updateLogPath) {
		t.Fatalf("properties=%v", props)
	}
	_, err = startSystemdUpdates(context.Background(), f, true)
	var failure *rebootError
	if !errors.As(err, &failure) || failure.code != 409 || len(f.started) != 1 {
		t.Fatalf("duplicate err=%v started=%v", err, f.started)
	}
}

func TestHostUpdatesReportOutputAndResultFromTheHostLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.log")
	os.WriteFile(path, []byte(updateMarker+" started 100\nHit:1 http://deb.debian.org trixie InRelease\n\nUnpacking tailscale (1.104.5)\r\nSetting up tailscale (1.104.5)\n"+updateMarker+" finished 160 100\n"), 0o644)
	var status updateStatus
	readUpdateLog(path, &status)
	if status.StartedAt != 100 || status.FinishedAt != 160 || status.ExitStatus == nil || *status.ExitStatus != 100 || len(status.Output) != 3 || status.Output[1] != "Unpacking tailscale (1.104.5)" {
		t.Fatalf("status=%+v", status)
	}
	running := updateStatus{Running: true}
	readUpdateLog(path, &running)
	if running.FinishedAt != 0 || running.ExitStatus != nil || running.StartedAt != 100 {
		t.Fatalf("running=%+v", running)
	}
	os.WriteFile(path, []byte(updateMarker+" started 200\nReading package lists...\n"), 0o644)
	var interrupted updateStatus
	readUpdateLog(path, &interrupted)
	if interrupted.StartedAt != 200 || interrupted.FinishedAt != 0 || interrupted.ExitStatus != nil {
		t.Fatalf("interrupted=%+v", interrupted)
	}
}

func TestHostUpdatesProxyRoundTripRefreshesCountsWhenFinished(t *testing.T) {
	f := &fakeSystemd{activeState: "inactive"}
	proxy := httptest.NewServer(&hostUpdater{hostname: "host", run: func(ctx context.Context, install bool) (updateStatus, error) {
		return startSystemdUpdates(ctx, f, install)
	}})
	defer proxy.Close()
	logFile := filepath.Join(t.TempDir(), "updates.log")
	var refreshed atomic.Int32
	u := &hostUpdater{hostname: "vm1", logFile: logFile, finished: func() { refreshed.Add(1) }, run: remoteHostAction(proxy.URL, "host updates", func(code int, status updateStatus) bool {
		return code == http.StatusAccepted && status.Running
	})}
	if w := updateRequest(u, http.MethodPost, `{"hostname":"other"}`); w.Code != 400 || len(f.started) != 0 {
		t.Fatalf("unconfirmed: %d %s", w.Code, w.Body)
	}
	if w := updateRequest(u, http.MethodPost, `{"hostname":"vm1"}`); w.Code != 202 || !strings.Contains(w.Body.String(), `"running":true`) {
		t.Fatalf("accepted: %d %s", w.Code, w.Body)
	}
	if w := updateRequest(u, http.MethodPost, `{"hostname":"vm1"}`); w.Code != 409 || len(f.started) != 1 {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body)
	}
	f.activeState = "inactive"
	os.WriteFile(logFile, []byte(updateMarker+" started 100\nSetting up tailscale\n"+updateMarker+" finished 160 0\n"), 0o644)
	for range 2 {
		w := updateRequest(u, http.MethodGet, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"exitStatus":0`) || !strings.Contains(w.Body.String(), "Setting up tailscale") {
			t.Fatalf("finished: %d %s", w.Code, w.Body)
		}
	}
	deadline := time.Now().Add(time.Second)
	for refreshed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("refreshed %d times", refreshed.Load())
	}
}

func TestHostUpdatesDisabledUnlessEnabledOnAnAptHost(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MONITOR_UPDATES_URL", "http://docker-proxy:2375/host/updates")
	t.Setenv("MONITOR_UPDATES_ENABLED", "false")
	if u := configuredUpdater("vm1", root, nil); u.reason == "" || u.run != nil {
		t.Fatalf("enabled without opt-in: %+v", u)
	}
	t.Setenv("MONITOR_UPDATES_ENABLED", "true")
	u := configuredUpdater("vm1", root, nil)
	if !strings.Contains(u.reason, "apt") || u.run != nil {
		t.Fatalf("enabled without apt: %+v", u)
	}
	if w := updateRequest(u, http.MethodPost, `{"hostname":"vm1"}`); w.Code != 403 {
		t.Fatalf("disabled install returned %d", w.Code)
	}
	os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(root, "usr/bin/apt-get"), nil, 0o755)
	if u := configuredUpdater("vm1", root, nil); u.reason != "" || u.run == nil {
		t.Fatalf("apt host stayed disabled: %+v", u)
	}
}
