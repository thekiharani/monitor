package host

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
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
