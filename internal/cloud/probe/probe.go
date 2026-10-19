package probe

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/thekiharani/monitor/internal/checks"
)

const (
	TokenPrefix     = "nmon_prb_"
	ClaimPath       = "/api/probe/v1/claim"
	ResultsPath     = "/api/probe/v1/results"
	MaxClaim        = 256
	MaxReport       = 500
	pollInterval    = time.Second
	flushInterval   = time.Second
	maxBackoff      = 30 * time.Second
	requestTimeout  = 20 * time.Second
	responseLimit   = 8 << 20
	reportQueueSize = 4 * MaxReport
)

type Check struct {
	MonitorID        string            `json:"monitor_id"`
	Kind             string            `json:"kind"`
	URL              string            `json:"url,omitempty"`
	Method           string            `json:"method,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	Body             string            `json:"body,omitempty"`
	ExpectedStatuses []int             `json:"expected_statuses,omitempty"`
	Keyword          string            `json:"keyword,omitempty"`
	KeywordAbsent    bool              `json:"keyword_absent,omitempty"`
	FollowRedirects  bool              `json:"follow_redirects"`
	TimeoutMs        int               `json:"timeout_ms"`
	PublicOnly       bool              `json:"public_only"`
}

func CheckFromSpec(monitorID string, spec checks.Spec) Check {
	return Check{
		MonitorID: monitorID, Kind: spec.Kind, URL: spec.URL, Method: spec.Method, Headers: spec.Headers, Body: spec.Body,
		ExpectedStatuses: spec.ExpectedStatuses, Keyword: spec.Keyword, KeywordAbsent: spec.KeywordAbsent,
		FollowRedirects: spec.FollowRedirects, TimeoutMs: int(spec.Timeout / time.Millisecond), PublicOnly: spec.PublicOnly,
	}
}

func (c Check) Spec() checks.Spec {
	return checks.Spec{
		Kind: c.Kind, URL: c.URL, Method: c.Method, Headers: c.Headers, Body: c.Body,
		ExpectedStatuses: c.ExpectedStatuses, Keyword: c.Keyword, KeywordAbsent: c.KeywordAbsent,
		FollowRedirects: c.FollowRedirects, Timeout: time.Duration(c.TimeoutMs) * time.Millisecond, PublicOnly: c.PublicOnly,
	}
}

type Claim struct {
	Limit   int    `json:"limit"`
	Version string `json:"version,omitempty"`
}

type Work struct {
	Region string  `json:"region"`
	Checks []Check `json:"checks"`
}

type Report struct {
	MonitorID     string     `json:"monitor_id"`
	CheckedAt     time.Time  `json:"checked_at"`
	Up            bool       `json:"up"`
	StatusCode    int        `json:"status_code,omitempty"`
	Error         string     `json:"error,omitempty"`
	TotalMs       float64    `json:"total_ms,omitempty"`
	DNSMs         float64    `json:"dns_ms,omitempty"`
	ConnectMs     float64    `json:"connect_ms,omitempty"`
	TLSMs         float64    `json:"tls_ms,omitempty"`
	TTFBMs        float64    `json:"ttfb_ms,omitempty"`
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
}

func ReportFrom(monitorID string, checkedAt time.Time, r checks.Result) Report {
	return Report{
		MonitorID: monitorID, CheckedAt: checkedAt, Up: r.Up, StatusCode: r.StatusCode, Error: r.Error,
		TotalMs: r.TotalMs, DNSMs: r.DNSMs, ConnectMs: r.ConnectMs, TLSMs: r.TLSMs, TTFBMs: r.TTFBMs, CertExpiresAt: r.CertExpiresAt,
	}
}

func (r Report) Result() checks.Result {
	return checks.Result{
		Up: r.Up, StatusCode: r.StatusCode, Error: r.Error, TotalMs: r.TotalMs, DNSMs: r.DNSMs,
		ConnectMs: r.ConnectMs, TLSMs: r.TLSMs, TTFBMs: r.TTFBMs, CertExpiresAt: r.CertExpiresAt,
	}
}

type Batch struct {
	Results []Report `json:"results"`
}

type Agent struct {
	Endpoint     string
	Token        string
	Version      string
	Concurrency  int
	AllowPrivate bool
	Client       *http.Client
	Logger       *slog.Logger
	Run          func(context.Context, checks.Spec) checks.Result
}

var ErrUnauthorized = errors.New("probe token rejected")

func (a Agent) Start(ctx context.Context) error {
	if a.Concurrency < 1 {
		a.Concurrency = 32
	}
	if a.Client == nil {
		a.Client = &http.Client{Timeout: requestTimeout}
	}
	if a.Logger == nil {
		a.Logger = slog.Default()
	}
	if a.Run == nil {
		a.Run = checks.Run
	}
	reports := make(chan Report, reportQueueSize)
	rejected := make(chan error, 1)
	reporterDone := make(chan struct{})
	go func() {
		defer close(reporterDone)
		a.report(context.WithoutCancel(ctx), reports, rejected)
	}()

	slots := make(chan struct{}, a.Concurrency)
	var running sync.WaitGroup
	backoff := pollInterval
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var loopErr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-rejected:
			loopErr = err
			break loop
		case <-ticker.C:
		}
		free := cap(slots) - len(slots)
		if free == 0 {
			continue
		}
		work, err := a.claim(ctx, min(free, MaxClaim))
		if errors.Is(err, ErrUnauthorized) {
			loopErr = err
			break loop
		}
		if err != nil {
			if ctx.Err() == nil {
				a.Logger.Warn("probe claim failed", "error", err.Error(), "retry_in", backoff.String())
				ticker.Reset(backoff)
				backoff = min(backoff*2, maxBackoff)
			}
			continue
		}
		if backoff != pollInterval {
			backoff = pollInterval
			ticker.Reset(pollInterval)
		}
		for _, check := range work.Checks {
			slots <- struct{}{}
			running.Go(func() {
				defer func() { <-slots }()
				spec := check.Spec()
				spec.PublicOnly = spec.PublicOnly || !a.AllowPrivate
				checkedAt := time.Now().UTC()
				result := a.Run(ctx, spec)
				if ctx.Err() != nil {
					return
				}
				reports <- ReportFrom(check.MonitorID, checkedAt, result)
			})
		}
	}
	running.Wait()
	close(reports)
	<-reporterDone
	if loopErr == nil {
		select {
		case loopErr = <-rejected:
		default:
		}
	}
	return loopErr
}

func (a Agent) report(ctx context.Context, reports <-chan Report, rejected chan<- error) {
	pending := make([]Report, 0, MaxReport)
	refused := false
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	flush := func() {
		for len(pending) > 0 && !refused {
			batch := pending[:min(len(pending), MaxReport)]
			err := a.post(ctx, ResultsPath, Batch{Results: batch}, nil)
			if errors.Is(err, ErrUnauthorized) {
				refused = true
				pending = pending[:0]
				select {
				case rejected <- err:
				default:
				}
				return
			}
			if err != nil {
				a.Logger.Warn("probe report failed", "error", err.Error(), "pending", len(pending))
				return
			}
			pending = pending[len(batch):]
		}
	}
	for {
		select {
		case report, open := <-reports:
			if !open {
				flush()
				return
			}
			if refused {
				continue
			}
			pending = append(pending, report)
			if len(pending) >= MaxReport {
				flush()
			}
			if len(pending) > reportQueueSize {
				pending = pending[len(pending)-reportQueueSize:]
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (a Agent) claim(ctx context.Context, limit int) (Work, error) {
	var work Work
	err := a.post(ctx, ClaimPath, Claim{Limit: limit, Version: a.Version}, &work)
	return work, err
}

func (a Agent) post(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.Endpoint, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "NoriaMonitorProbe/"+a.Version)
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrUnauthorized, strings.TrimSpace(string(body)))
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s answered %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	case out == nil:
		return nil
	}
	return json.Unmarshal(body, out)
}
