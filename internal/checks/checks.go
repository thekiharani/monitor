package checks

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	KindHTTP      = "http"
	KindKeyword   = "keyword"
	KindTCP       = "tcp"
	KindHeartbeat = "heartbeat"

	maxBody      = 1 << 20
	maxRedirects = 5
	userAgent    = "NoriaMonitor/1.0 (+https://monitor.noria.co.ke)"
)

var Methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}

var errPrivateAddress = errors.New("private address")

type Spec struct {
	Kind             string
	URL              string
	Method           string
	Headers          map[string]string
	Body             string
	ExpectedStatuses []int
	Keyword          string
	KeywordAbsent    bool
	FollowRedirects  bool
	Timeout          time.Duration
	PublicOnly       bool
}

type Result struct {
	Up            bool
	StatusCode    int
	Error         string
	TotalMs       float64
	DNSMs         float64
	ConnectMs     float64
	TLSMs         float64
	TTFBMs        float64
	CertExpiresAt *time.Time
}

func Run(ctx context.Context, spec Spec) Result {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch spec.Kind {
	case KindTCP:
		return runTCP(ctx, spec)
	case KindHTTP, KindKeyword:
		return runHTTP(ctx, spec)
	}
	return Result{Error: fmt.Sprintf("unsupported monitor kind %q", spec.Kind)}
}

func runTCP(ctx context.Context, spec Spec) Result {
	u, err := url.Parse(spec.URL)
	if err != nil || u.Scheme != "tcp" || u.Port() == "" {
		return Result{Error: "an invalid address"}
	}
	start := time.Now()
	conn, err := dialer(spec.PublicOnly).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return Result{Error: Describe(err)}
	}
	conn.Close()
	elapsed := ms(time.Since(start))
	return Result{Up: true, TotalMs: elapsed, ConnectMs: elapsed}
}

func runHTTP(ctx context.Context, spec Spec) Result {
	method := spec.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if spec.Body != "" {
		body = strings.NewReader(spec.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, spec.URL, body)
	if err != nil {
		return Result{Error: "an invalid URL"}
	}
	req.Header.Set("User-Agent", userAgent)
	for name, value := range spec.Headers {
		req.Header.Set(name, value)
	}

	var res Result
	var dnsStart, connectStart, tlsStart, wroteAt time.Time
	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:           func(httptrace.DNSDoneInfo) { res.DNSMs = since(dnsStart) },
		ConnectStart:      func(string, string) { connectStart = time.Now() },
		ConnectDone:       func(string, string, error) { res.ConnectMs = since(connectStart) },
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { res.TLSMs = since(tlsStart) },
		WroteRequest:      func(httptrace.WroteRequestInfo) { wroteAt = time.Now() },
		GotFirstResponseByte: func() {
			res.TTFBMs = since(wroteAt)
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	start := time.Now()
	resp, err := client(spec).Do(req)
	if err != nil {
		res.Error = Describe(err)
		return res
	}
	defer resp.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.TotalMs = ms(time.Since(start))
	res.StatusCode = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		expires := resp.TLS.PeerCertificates[0].NotAfter.UTC()
		res.CertExpiresAt = &expires
	}

	if !statusAccepted(resp.StatusCode, spec.ExpectedStatuses) {
		res.Error = fmt.Sprintf("returning HTTP %d", resp.StatusCode)
		return res
	}
	if spec.Kind == KindKeyword {
		if readErr != nil {
			res.Error = Describe(readErr)
			return res
		}
		found := bytes.Contains(content, []byte(spec.Keyword))
		switch {
		case spec.KeywordAbsent && found:
			res.Error = fmt.Sprintf("containing %q", spec.Keyword)
			return res
		case !spec.KeywordAbsent && !found:
			res.Error = fmt.Sprintf("missing %q", spec.Keyword)
			return res
		}
	}
	res.Up = true
	return res
}

func client(spec Spec) *http.Client {
	transport := &http.Transport{
		DialContext:           dialer(spec.PublicOnly).DialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   spec.Timeout,
		ResponseHeaderTimeout: spec.Timeout,
		ForceAttemptHTTP2:     true,
	}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if spec.FollowRedirects {
		redirect = func(_ *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			return nil
		}
	}
	return &http.Client{Transport: transport, CheckRedirect: redirect}
}

func dialer(publicOnly bool) *net.Dialer {
	d := &net.Dialer{}
	if publicOnly {
		d.Control = refusePrivate
	}
	return d
}

func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errPrivateAddress
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !PublicAddr(addr) {
		return errPrivateAddress
	}
	return nil
}

func statusAccepted(code int, expected []int) bool {
	if len(expected) == 0 {
		return code >= 200 && code < 400
	}
	return slices.Contains(expected, code)
}

func PublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return false
	}
	for _, reserved := range reservedRanges {
		if reserved.Contains(addr) {
			return false
		}
	}
	return true
}

var reservedRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func Describe(err error) string {
	if errors.Is(err, errPrivateAddress) {
		return "pointing at a private address"
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return "presenting a certificate from an unknown authority"
	}
	if hostErr, ok := errors.AsType[x509.HostnameError](err); ok {
		return "presenting a certificate for " + strings.Join(hostErr.Certificate.DNSNames, ", ")
	}
	if invalid, ok := errors.AsType[x509.CertificateInvalidError](err); ok && invalid.Reason == x509.Expired {
		return "presenting an expired certificate"
	}
	netErr, isNet := errors.AsType[net.Error](err)
	msg := err.Error()
	switch {
	case isNet && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "not responding"
	case strings.Contains(msg, "no such host"):
		return "not resolving"
	case strings.Contains(msg, "connection refused"):
		return "refusing connections"
	case strings.Contains(msg, "connection reset"):
		return "resetting the connection"
	case strings.Contains(msg, "too many redirects"):
		return "redirecting too many times"
	case strings.Contains(msg, "x509:"), strings.Contains(msg, "tls:"):
		return "presenting an invalid certificate"
	}
	return msg
}

func NormalizeTarget(kind, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 2048 {
		return "", errors.New("the target must be 1 to 2048 characters")
	}
	if kind == KindTCP && !strings.Contains(trimmed, "://") {
		trimmed = "tcp://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("the target is not a valid address")
	}
	switch kind {
	case KindTCP:
		port, err := strconv.Atoi(u.Port())
		if u.Scheme != "tcp" || err != nil || port < 1 || port > 65535 || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return "", errors.New("a TCP target is host:port")
		}
		return "tcp://" + u.Host, nil
	case KindHTTP, KindKeyword:
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", errors.New("an HTTP target starts with http:// or https://")
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("unsupported monitor kind %q", kind)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func since(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return ms(time.Since(t))
}
