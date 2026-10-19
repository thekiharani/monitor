package host

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthcheckSupportsNativeAndContainerAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{addr, ":" + port, "0.0.0.0:" + port} {
		if code := probe(addr); code != 0 {
			t.Errorf("healthcheck %s: exit %d", addr, code)
		}
	}
	if probe("invalid") != 1 {
		t.Fatal("malformed address must fail")
	}
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer failed.Close()
	if probe(strings.TrimPrefix(failed.URL, "http://")) != 1 {
		t.Fatal("unhealthy server must fail")
	}
}

func TestNativeAndContainerDefaults(t *testing.T) {
	native := defaultRuntimePaths(false, "/user/config")
	if native.proc != "/proc" || native.sys != "/sys" || native.root != "/" || native.addr != "127.0.0.1:9100" || native.data != "/user/config/monitor" {
		t.Fatalf("native defaults: %+v", native)
	}
	container := defaultRuntimePaths(true, "")
	if container.proc != "/host/proc" || container.sys != "/host/sys" || container.root != "/host/root" || container.addr != ":8080" || container.data != "/data" {
		t.Fatalf("container defaults changed: %+v", container)
	}
}

func TestExplicitRuntimePathsOverrideDefaults(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	for name, value := range map[string]string{
		"MONITOR_DATA_DIR": data, "ADDR": "127.0.0.1:1234",
		"HOST_PROC": "/custom/proc", "HOST_SYS": "/custom/sys", "HOST_ROOT": "/custom/root",
	} {
		t.Setenv(name, value)
	}
	p, err := configuredRuntimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.data != data || p.addr != "127.0.0.1:1234" || p.proc != "/custom/proc" || p.sys != "/custom/sys" || p.root != "/custom/root" {
		t.Fatalf("overrides lost: %+v", p)
	}
}
