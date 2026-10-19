package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HostResult struct {
	Up          bool
	Error       string
	Status      int
	Latency     float64
	CertExpires int64
}

func RunHost(ctx context.Context, client *http.Client, target string) HostResult {
	ck := HostResult{}
	u, err := url.Parse(target)
	if err != nil {
		ck.Error = "an invalid URL"
		return ck
	}
	start := time.Now()
	switch u.Scheme {
	case "tcp":
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			ck.Error = DescribeHost(err)
			return ck
		}
		conn.Close()
	case "http", "https":
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		resp, err := client.Do(req)
		if err != nil {
			ck.Error = DescribeHost(err)
			return ck
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		ck.Status = resp.StatusCode
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			ck.CertExpires = resp.TLS.PeerCertificates[0].NotAfter.Unix()
		}
		if resp.StatusCode >= 400 {
			ck.Error = fmt.Sprintf("returning HTTP %d", resp.StatusCode)
			return ck
		}
	default:
		ck.Error = fmt.Sprintf("using unsupported scheme %q", u.Scheme)
		return ck
	}
	ck.Latency = float64(time.Since(start).Microseconds()) / 1000
	ck.Up = true
	return ck
}

func DescribeHost(err error) string {
	netErr, isNet := errors.AsType[net.Error](err)
	msg := err.Error()
	switch {
	case isNet && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded), strings.Contains(msg, "server selection timeout"):
		return "not responding"
	case strings.Contains(msg, "password authentication failed"), strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "NOAUTH"),
		strings.Contains(msg, "Access denied"), strings.Contains(msg, "AuthenticationFailed"), strings.Contains(msg, "HTTP 401"):
		return "rejecting the login"
	case strings.Contains(msg, "connection refused"):
		return "refusing connections"
	case strings.Contains(msg, "no such host"):
		return "failing DNS lookup"
	case strings.Contains(msg, "no such file or directory"):
		return "missing a required file or socket"
	case strings.Contains(msg, "x509:"):
		return "presenting an invalid certificate"
	}
	return msg
}
