package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var mirroredSurface = []struct {
	method    string
	dashboard string
	api       string
}{
	{http.MethodGet, "/api/dashboard/v1/me", "/api/v1/me"},
	{http.MethodGet, "/api/dashboard/v1/monitors", "/api/v1/monitors"},
	{http.MethodPost, "/api/dashboard/v1/monitors", "/api/v1/monitors"},
	{http.MethodGet, "/api/dashboard/v1/monitors/abc", "/api/v1/monitors/abc"},
	{http.MethodPatch, "/api/dashboard/v1/monitors/abc", "/api/v1/monitors/abc"},
	{http.MethodDelete, "/api/dashboard/v1/monitors/abc", "/api/v1/monitors/abc"},
	{http.MethodGet, "/api/dashboard/v1/monitors/abc/results", "/api/v1/monitors/abc/results"},
	{http.MethodGet, "/api/dashboard/v1/monitors/abc/series", "/api/v1/monitors/abc/series"},
	{http.MethodGet, "/api/dashboard/v1/monitors/abc/daily", "/api/v1/monitors/abc/daily"},
	{http.MethodGet, "/api/dashboard/v1/regions", "/api/v1/regions"},
	{http.MethodGet, "/api/dashboard/v1/status-pages", "/api/v1/status-pages"},
	{http.MethodPost, "/api/dashboard/v1/status-pages", "/api/v1/status-pages"},
	{http.MethodGet, "/api/dashboard/v1/status-pages/abc", "/api/v1/status-pages/abc"},
	{http.MethodPatch, "/api/dashboard/v1/status-pages/abc", "/api/v1/status-pages/abc"},
	{http.MethodDelete, "/api/dashboard/v1/status-pages/abc", "/api/v1/status-pages/abc"},
	{http.MethodPut, "/api/dashboard/v1/status-pages/abc/components", "/api/v1/status-pages/abc/components"},
	{http.MethodPost, "/api/dashboard/v1/status-pages/abc/domain/verify", "/api/v1/status-pages/abc/domain/verify"},
	{http.MethodGet, "/api/dashboard/v1/status-pages/abc/posts", "/api/v1/status-pages/abc/posts"},
	{http.MethodPost, "/api/dashboard/v1/status-pages/abc/posts", "/api/v1/status-pages/abc/posts"},
	{http.MethodPost, "/api/dashboard/v1/status-pages/abc/posts/def/updates", "/api/v1/status-pages/abc/posts/def/updates"},
	{http.MethodDelete, "/api/dashboard/v1/status-pages/abc/posts/def", "/api/v1/status-pages/abc/posts/def"},
	{http.MethodGet, "/api/dashboard/v1/alert-channels", "/api/v1/alert-channels"},
	{http.MethodPost, "/api/dashboard/v1/alert-channels", "/api/v1/alert-channels"},
	{http.MethodPatch, "/api/dashboard/v1/alert-channels/abc", "/api/v1/alert-channels/abc"},
	{http.MethodDelete, "/api/dashboard/v1/alert-channels/abc", "/api/v1/alert-channels/abc"},
	{http.MethodPost, "/api/dashboard/v1/alert-channels/abc/test", "/api/v1/alert-channels/abc/test"},
	{http.MethodGet, "/api/dashboard/v1/alert-channels/abc/deliveries", "/api/v1/alert-channels/abc/deliveries"},
	{http.MethodGet, "/api/dashboard/v1/incidents", "/api/v1/incidents"},
	{http.MethodPost, "/api/dashboard/v1/incidents/abc/acknowledge", "/api/v1/incidents/abc/acknowledge"},
	{http.MethodGet, "/api/dashboard/v1/maintenance", "/api/v1/maintenance"},
	{http.MethodPost, "/api/dashboard/v1/maintenance", "/api/v1/maintenance"},
	{http.MethodDelete, "/api/dashboard/v1/maintenance/abc", "/api/v1/maintenance/abc"},
	{http.MethodPatch, "/api/dashboard/v1/me", "/api/v1/me"},
	{http.MethodPost, "/api/dashboard/v1/me/email", "/api/v1/me/email"},
	{http.MethodPost, "/api/dashboard/v1/me/email/verify", "/api/v1/me/email/verify"},
	{http.MethodPatch, "/api/dashboard/v1/account", "/api/v1/account"},
	{http.MethodPost, "/api/dashboard/v1/account/close", "/api/v1/account/close"},
	{http.MethodGet, "/api/dashboard/v1/accounts", "/api/v1/accounts"},
	{http.MethodGet, "/api/dashboard/v1/members", "/api/v1/members"},
	{http.MethodPost, "/api/dashboard/v1/members/invites", "/api/v1/members/invites"},
	{http.MethodDelete, "/api/dashboard/v1/members/invites/abc", "/api/v1/members/invites/abc"},
	{http.MethodPost, "/api/dashboard/v1/members/invites/abc/resend", "/api/v1/members/invites/abc/resend"},
	{http.MethodPost, "/api/dashboard/v1/members/invites/abc/accept", "/api/v1/members/invites/abc/accept"},
	{http.MethodPatch, "/api/dashboard/v1/members/abc", "/api/v1/members/abc"},
	{http.MethodDelete, "/api/dashboard/v1/members/abc", "/api/v1/members/abc"},
	{http.MethodGet, "/api/dashboard/v1/api-keys", "/api/v1/api-keys"},
	{http.MethodPost, "/api/dashboard/v1/api-keys", "/api/v1/api-keys"},
	{http.MethodDelete, "/api/dashboard/v1/api-keys/abc", "/api/v1/api-keys/abc"},
	{http.MethodGet, "/api/dashboard/v1/billing", "/api/v1/billing"},
	{http.MethodPost, "/api/dashboard/v1/billing/checkout", "/api/v1/billing/checkout"},
	{http.MethodPost, "/api/dashboard/v1/billing/verify", "/api/v1/billing/verify"},
	{http.MethodPost, "/api/dashboard/v1/billing/renew", "/api/v1/billing/renew"},
	{http.MethodPost, "/api/dashboard/v1/billing/resume", "/api/v1/billing/resume"},
	{http.MethodPost, "/api/dashboard/v1/billing/change-plan", "/api/v1/billing/change-plan"},
	{http.MethodPost, "/api/dashboard/v1/billing/cancel", "/api/v1/billing/cancel"},
	{http.MethodPost, "/api/dashboard/v1/billing/manage-payment", "/api/v1/billing/manage-payment"},
	{http.MethodGet, "/api/dashboard/v1/usage", "/api/v1/usage"},
}

func matchedPattern(mux *http.ServeMux, method, path string) string {
	req := httptest.NewRequest(method, path, nil)
	_, pattern := mux.Handler(req)
	return pattern
}

func TestEveryDashboardOperationHasABearerTwin(t *testing.T) {
	a := &App{cfg: testConfig()}
	mux := a.routes()
	for _, route := range mirroredSurface {
		dashboard := matchedPattern(mux, route.method, route.dashboard)
		if dashboard == "" || dashboard == "/" {
			t.Fatalf("%s %s is not registered at all", route.method, route.dashboard)
		}
		api := matchedPattern(mux, route.method, route.api)
		if api == "" || api == "/" {
			t.Errorf("%s %s has no bearer twin; %s resolves to %q but %s falls through",
				route.method, route.dashboard, route.dashboard, dashboard, route.api)
		}
	}
}

func TestBearerTwinsShareTheirDashboardHandler(t *testing.T) {
	a := &App{cfg: testConfig()}
	mux := a.routes()
	for _, route := range mirroredSurface {
		dashboardHandler, _ := mux.Handler(httptest.NewRequest(route.method, route.dashboard, nil))
		apiHandler, _ := mux.Handler(httptest.NewRequest(route.method, route.api, nil))
		if handlerName(dashboardHandler) != handlerName(apiHandler) {
			t.Errorf("%s %s is served by %s but %s is served by %s; the two surfaces have drifted apart",
				route.method, route.dashboard, handlerName(dashboardHandler), route.api, handlerName(apiHandler))
		}
	}
}

func TestSignInIsReachableOnBothOrigins(t *testing.T) {
	a := &App{cfg: testConfig()}
	mux := a.routes()
	for _, path := range []string{"/auth/otp/request", "/auth/otp/verify", "/api/v1/auth/otp/request", "/api/v1/auth/otp/verify"} {
		if pattern := matchedPattern(mux, http.MethodPost, path); pattern == "" || pattern == "/" {
			t.Errorf("POST %s is not registered", path)
		}
	}
}

func TestAccountSwitchingStaysOnTheSessionSurface(t *testing.T) {
	a := &App{cfg: testConfig()}
	mux := a.routes()
	if pattern := matchedPattern(mux, http.MethodPost, "/api/v1/accounts/abc/switch"); pattern != "" && pattern != "/" {
		t.Errorf("POST /api/v1/accounts/{id}/switch resolves to %q; a bearer key is bound to one workspace and mints a new key instead of swapping a cookie", pattern)
	}
}

func TestJSONAPICoversBothSurfaces(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/billing":            true,
		"/api/dashboard/v1/billing":  true,
		"/billing/paystack/callback": false,
		"/webhooks/paystack":         false,
		"/auth/otp/verify":           false,
		"/api/control/v1/stats":      true,
	}
	for path, want := range cases {
		if got := isJSONAPI(httptest.NewRequest(http.MethodGet, path, nil)); got != want {
			t.Errorf("isJSONAPI(%s)=%v, want %v", path, got, want)
		}
	}
}

func handlerName(h http.Handler) string {
	if h == nil {
		return "<nil>"
	}
	name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	name = strings.TrimSuffix(name, "-fm")
	if _, method, found := strings.CutLast(name, "."); found {
		return method
	}
	return name
}
