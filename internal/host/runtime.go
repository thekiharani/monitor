package host

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type hostCollector interface {
	HostInfo() HostInfo
	Sample(time.Time) System
}

type runtimePaths struct {
	proc, sys, root, data, addr string
}

func defaultRuntimePaths(container bool, configDir string) runtimePaths {
	if container {
		return runtimePaths{"/host/proc", "/host/sys", "/host/root", "/data", ":8080"}
	}
	return runtimePaths{"/proc", "/sys", "/", filepath.Join(configDir, "monitor"), "127.0.0.1:9100"}
}

func inContainer() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, path := range []string{"/.dockerenv", "/run/.containerenv", "/host/proc/stat"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func configuredRuntimePaths() (runtimePaths, error) {
	container := inContainer()
	var configDir string
	if !container && os.Getenv("MONITOR_DATA_DIR") == "" {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			return runtimePaths{}, fmt.Errorf("locate monitor data directory (or set MONITOR_DATA_DIR): %w", err)
		}
	}
	p := defaultRuntimePaths(container, configDir)
	p.proc, p.sys, p.root = env("HOST_PROC", p.proc), env("HOST_SYS", p.sys), env("HOST_ROOT", p.root)
	p.data, p.addr = env("MONITOR_DATA_DIR", p.data), env("ADDR", p.addr)
	return p, nil
}
