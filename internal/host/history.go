package host

import (
	"sync"
	"time"
)

const (
	secondSize  = 30 * 60
	minuteSize  = 24 * 60
	chartPoints = 240
)

var Ranges = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"3h":  3 * time.Hour,
	"6h":  6 * time.Hour,
	"12h": 12 * time.Hour,
	"24h": 24 * time.Hour,
}

type Point struct {
	T        int64   `json:"t"`
	CPU      float64 `json:"cpu"`
	CPUMax   float64 `json:"cpuMax"`
	IOWait   float64 `json:"iowait"`
	Steal    float64 `json:"steal"`
	Mem      float64 `json:"mem"`
	Load     float64 `json:"load"`
	PsiCPU   float64 `json:"psiCpu"`
	PsiMem   float64 `json:"psiMem"`
	PsiIO    float64 `json:"psiIo"`
	ReadBps  float64 `json:"readBps"`
	WriteBps float64 `json:"writeBps"`
	RxBps    float64 `json:"rxBps"`
	TxBps    float64 `json:"txBps"`
	ReadOps  float64 `json:"readOps"`
	WriteOps float64 `json:"writeOps"`
	ReadLat  float64 `json:"readLat"`
	WriteLat float64 `json:"writeLat"`
	Queue    float64 `json:"queue"`
	Level    int     `json:"level,omitzero"`
	Issue    string  `json:"issue,omitempty"`
}

func pointOf(t time.Time, s System) Point {
	p := Point{
		T:        t.UnixMilli(),
		CPU:      s.CPU.Total,
		CPUMax:   s.CPU.Total,
		IOWait:   s.CPU.IOWait,
		Steal:    s.CPU.Steal,
		Load:     s.Load[0],
		ReadBps:  s.Disk.ReadBps,
		WriteBps: s.Disk.WriteBps,
		ReadOps:  s.Disk.ReadIOPS,
		WriteOps: s.Disk.WriteIOPS,
		ReadLat:  s.Disk.ReadLatency,
		WriteLat: s.Disk.WriteLatency,
		Queue:    s.Disk.Queue,
	}
	if s.Mem.Total > 0 {
		p.Mem = 100 * float64(s.Mem.Used) / float64(s.Mem.Total)
	}
	if s.Pressure != nil {
		p.PsiCPU, p.PsiMem, p.PsiIO = s.Pressure.CPU, s.Pressure.Memory, s.Pressure.IO
	}
	for _, n := range s.Net {
		p.RxBps += n.RxBps
		p.TxBps += n.TxBps
	}
	return p
}

func average(points []Point) Point {
	var a Point
	if len(points) == 0 {
		return a
	}
	for _, p := range points {
		if p.Level > a.Level {
			a.Level, a.Issue = p.Level, p.Issue
		}
		a.CPU += p.CPU
		a.CPUMax = max(a.CPUMax, p.CPUMax)
		a.IOWait += p.IOWait
		a.Steal += p.Steal
		a.Mem += p.Mem
		a.Load += p.Load
		a.PsiCPU += p.PsiCPU
		a.PsiMem += p.PsiMem
		a.PsiIO += p.PsiIO
		a.ReadBps += p.ReadBps
		a.WriteBps += p.WriteBps
		a.RxBps += p.RxBps
		a.TxBps += p.TxBps
		a.ReadOps += p.ReadOps
		a.WriteOps += p.WriteOps
		a.ReadLat += p.ReadLat * p.ReadOps
		a.WriteLat += p.WriteLat * p.WriteOps
		a.Queue += p.Queue
	}
	if a.ReadOps > 0 {
		a.ReadLat /= a.ReadOps
	}
	if a.WriteOps > 0 {
		a.WriteLat /= a.WriteOps
	}
	n := float64(len(points))
	a.T = points[len(points)-1].T
	a.CPU /= n
	a.IOWait /= n
	a.Steal /= n
	a.Mem /= n
	a.Load /= n
	a.PsiCPU /= n
	a.PsiMem /= n
	a.PsiIO /= n
	a.ReadBps /= n
	a.WriteBps /= n
	a.RxBps /= n
	a.TxBps /= n
	a.ReadOps /= n
	a.WriteOps /= n
	a.Queue /= n
	return a
}

type History struct {
	mu      sync.RWMutex
	store   *MetricStore
	seconds []Point
	pending []Point
	minutes []Point
	minute  int64
}

func NewHistory() *History {
	return &History{}
}

func (h *History) Add(p Point) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seconds = append(h.seconds, p)
	if len(h.seconds) > secondSize {
		h.seconds = h.seconds[len(h.seconds)-secondSize:]
	}
	minute := p.T / 60000
	if h.minute != 0 && minute != h.minute && len(h.pending) > 0 {
		h.minutes = append(h.minutes, average(h.pending))
		if len(h.minutes) > minuteSize {
			h.minutes = h.minutes[len(h.minutes)-minuteSize:]
		}
		h.pending = h.pending[:0]
	}
	h.minute = minute
	h.pending = append(h.pending, p)
	if h.store != nil {
		recordMetric(h.store, "host", "", p.T, p, average(h.pending))
	}
}

func (h *History) Recent(n int) []Point {
	h.mu.RLock()
	defer h.mu.RUnlock()
	start := max(0, len(h.seconds)-n)
	cutoff := time.Now().Add(-time.Duration(n) * time.Second).UnixMilli()
	for start < len(h.seconds) && h.seconds[start].T < cutoff {
		start++
	}
	return append([]Point(nil), h.seconds[start:]...)
}

func (h *History) Range(name string) []Point {
	span, ok := Ranges[name]
	if !ok {
		span = 5 * time.Minute
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	source := h.minutes
	if span <= secondSize*time.Second {
		source = h.seconds
	}
	cutoff := time.Now().Add(-span).UnixMilli()
	start := len(source)
	for start > 0 && source[start-1].T > cutoff {
		start--
	}
	return downsample(source[start:], chartPoints, average)
}

func downsample[T any](points []T, limit int, avg func([]T) T) []T {
	if len(points) <= limit {
		return append([]T{}, points...)
	}
	size := (len(points) + limit - 1) / limit
	out := make([]T, 0, limit)
	for i := 0; i < len(points); i += size {
		out = append(out, avg(points[i:min(i+size, len(points))]))
	}
	return out
}
