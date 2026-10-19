package host

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
)

func TestDarwinCPUUsesSampleDeltas(t *testing.T) {
	before := []cpu.TimesStat{{User: 10, System: 10, Idle: 80}, {User: 10, System: 10, Idle: 80}}
	after := []cpu.TimesStat{{User: 20, Nice: 5, System: 15, Idle: 100}, {User: 10, System: 20, Idle: 110}}
	got := darwinCPU(before, after)
	if got.Total != 37.5 || got.User != 18.75 || got.System != 18.75 || got.Cores[0] != 50 || got.Cores[1] != 25 {
		t.Fatalf("CPU deltas: %+v", got)
	}
	for _, pair := range [][2][]cpu.TimesStat{{nil, after}, {after, before}, {before, before}} {
		if got := darwinCPU(pair[0], pair[1]); got.Total != 0 || math.IsNaN(got.Total) {
			t.Fatalf("first/reset/unchanged counters: %+v", got)
		}
	}
}

func TestDarwinDiskRatesAndLatency(t *testing.T) {
	before := map[string]disk.IOCountersStat{"disk0": {ReadBytes: 1000, WriteBytes: 2000, ReadCount: 10, WriteCount: 20, ReadTime: 100, WriteTime: 200}}
	after := map[string]disk.IOCountersStat{
		"disk0": {ReadBytes: 3000, WriteBytes: 6000, ReadCount: 20, WriteCount: 40, ReadTime: 150, WriteTime: 400},
		"disk1": {ReadBytes: 999999, ReadCount: 99999}, // A newly attached disk has no baseline.
	}
	got := darwinDiskIO(before, after, 2)
	if got.ReadBps != 1000 || got.WriteBps != 2000 || got.ReadIOPS != 5 || got.WriteIOPS != 10 || got.ReadLatency != 5 || got.WriteLatency != 10 || got.Busy != 0 || got.Queue != 0 {
		t.Fatalf("disk deltas: %+v", got)
	}
	for _, got := range []DiskIO{darwinDiskIO(nil, after, 2), darwinDiskIO(before, after, 0), darwinDiskIO(after, before, 2)} {
		if got != (DiskIO{}) {
			t.Fatalf("first/reset counters: %+v", got)
		}
	}
}

func TestDarwinSelectsWritableDataVolume(t *testing.T) {
	for _, tc := range []struct {
		path, fs, want string
		data, keep     bool
	}{
		{"/", "apfs", "", true, false},
		{"/System/Volumes/Data", "apfs", "/", true, true},
		{"/System/Volumes/Preboot", "apfs", "", true, false},
		{"/", "apfs", "/", false, true},
		{"/Volumes/External", "exfat", "/Volumes/External", true, true},
		{"/dev", "devfs", "", true, false},
	} {
		got, keep := darwinMount(disk.PartitionStat{Mountpoint: tc.path, Fstype: tc.fs}, tc.data)
		if got != tc.want || keep != tc.keep {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.path, got, keep, tc.want, tc.keep)
		}
	}
}

func TestDarwinCollectorSamplesNativeHost(t *testing.T) {
	c := newHostCollector("", "", "")
	info := c.HostInfo()
	if !strings.HasPrefix(info.OS, "macOS ") || info.Hostname == "" || info.Kernel == "" || info.Cores < 1 {
		t.Fatalf("native host identity: %+v", info)
	}
	s := c.Sample(time.Now())
	if s.Mem.Total == 0 || s.Mem.Used > s.Mem.Total || s.Uptime <= 0 || len(s.CPU.Cores) != info.Cores {
		t.Fatalf("invalid native metrics: %+v", s)
	}
	if s.Pressure != nil || s.TempC != 0 {
		t.Fatalf("Linux-only metrics should be unavailable: %+v", s)
	}
	var root *Filesystem
	for i := range s.Filesystems {
		if s.Filesystems[i].Mount == "/" {
			root = &s.Filesystems[i]
		}
	}
	if root == nil || root.Total == 0 || root.ReadOnly {
		t.Fatalf("missing writable macOS data volume: %+v", s.Filesystems)
	}
}
