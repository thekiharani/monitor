package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thekiharani/monitor/internal/checks"
)

const (
	EventDown        = "down"
	EventUp          = "up"
	EventCertificate = "certificate"
	EventTest        = "test"

	userAgent = "NoriaMonitor-Alerts/1.0"
)

type Event struct {
	Kind          string     `json:"event"`
	MonitorID     string     `json:"monitor_id,omitempty"`
	MonitorName   string     `json:"monitor_name"`
	Workspace     string     `json:"workspace,omitempty"`
	Target        string     `json:"target,omitempty"`
	DashboardURL  string     `json:"dashboard_url,omitempty"`
	IncidentID    string     `json:"incident_id,omitempty"`
	Cause         string     `json:"cause,omitempty"`
	RegionsDown   []string   `json:"regions_down,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
	ReplyTo       string     `json:"-"`
}

func (e Event) Downtime() string {
	if e.StartedAt == nil || e.ResolvedAt == nil {
		return ""
	}
	return HumanDuration(e.ResolvedAt.Sub(*e.StartedAt))
}

func HumanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func (e Event) Headline() string {
	switch e.Kind {
	case EventDown:
		return e.MonitorName + " is down"
	case EventUp:
		if downtime := e.Downtime(); downtime != "" {
			return e.MonitorName + " is back up after " + downtime
		}
		return e.MonitorName + " is back up"
	case EventCertificate:
		if e.CertExpiresAt != nil {
			days := int(time.Until(*e.CertExpiresAt).Hours() / 24)
			return fmt.Sprintf("%s's certificate expires in %d days", e.MonitorName, max(days, 0))
		}
		return e.MonitorName + "'s certificate expires soon"
	}
	return "Test alert from Noria Monitor"
}

func (e Event) Detail() string {
	switch e.Kind {
	case EventDown:
		detail := "It is " + e.Cause + "."
		if len(e.RegionsDown) > 0 {
			detail += " Confirmed from " + strings.Join(e.RegionsDown, ", ") + "."
		}
		return detail
	case EventUp:
		return "The incident is resolved."
	case EventCertificate:
		if e.CertExpiresAt != nil {
			return "It expires on " + e.CertExpiresAt.UTC().Format("2 January 2006") + ". Renew it before visitors see a warning."
		}
	}
	return "This channel is set up correctly. Real alerts will look like this."
}

type Delivery struct {
	MessageID string
}

type Channel interface {
	Send(context.Context, Event) (Delivery, error)
}

func Client(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errors.New("refusing an unparseable address")
			}
			addr, err := netip.ParseAddr(host)
			if err != nil || !checks.PublicAddr(addr) {
				return errors.New("refusing a private address")
			}
			return nil
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: timeout},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("alert endpoints must answer without redirecting")
		},
	}
}

func post(ctx context.Context, client *http.Client, url string, payload []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("answered %d: %s", resp.StatusCode, snippet(body))
	}
	return body, nil
}

func snippet(body []byte) string {
	text := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(body))
	text = strings.TrimSpace(text)
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

type Telegram struct {
	Token   string
	ChatID  string
	TopicID int64
	APIBase string
	Client  *http.Client
}

type telegramReply struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

func (t Telegram) call(ctx context.Context, method string, payload map[string]any) (telegramReply, error) {
	base := t.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return telegramReply{}, err
	}
	body, err := post(ctx, t.Client, base+"/bot"+t.Token+"/"+method, raw, nil)
	var reply telegramReply
	if body != nil {
		_ = json.Unmarshal(body, &reply)
	}
	if err != nil {
		if reply.Description != "" {
			return reply, errors.New("telegram: " + reply.Description)
		}
		return reply, fmt.Errorf("telegram %w", err)
	}
	return reply, nil
}

func shortTime(t time.Time) string {
	t = t.UTC()
	if time.Since(t) < 20*time.Hour {
		return t.Format("15:04") + " UTC"
	}
	return t.Format("2 Jan 15:04") + " UTC"
}

func telegramText(e Event, resolved bool) string {
	esc := html.EscapeString
	icon, label := "🔴", "DOWN"
	switch {
	case resolved || e.Kind == EventUp:
		icon, label = "✅", "RESOLVED"
	case e.Kind == EventCertificate:
		icon, label = "🟠", "CERTIFICATE"
	case e.Kind == EventTest:
		icon, label = "🧪", "TEST"
	}
	var b strings.Builder
	b.WriteString(icon + " <b>" + label + "</b>")
	if e.Workspace != "" {
		b.WriteString("  ·  <code>" + esc(e.Workspace) + "</code>")
	}
	b.WriteString("\n\n")
	switch {
	case e.Kind == EventTest:
		b.WriteString("<b>Alerts reach this chat</b>\nNothing is wrong. This is a test.")
		return b.String()
	case resolved || e.Kind == EventUp:
		meta := "back up"
		if downtime := e.Downtime(); downtime != "" {
			meta = "down for " + downtime
		}
		if e.ResolvedAt != nil {
			meta += " · recovered at " + shortTime(*e.ResolvedAt)
		}
		fmt.Fprintf(&b, "<b>%s</b>\n└ %s", esc(e.MonitorName), esc(meta))
	case e.Kind == EventCertificate:
		meta := "certificate expires soon"
		if e.CertExpiresAt != nil {
			meta = fmt.Sprintf("certificate expires in %d days · on %s", max(int(time.Until(*e.CertExpiresAt).Hours()/24), 0), e.CertExpiresAt.UTC().Format("2 Jan 2006"))
		}
		fmt.Fprintf(&b, "<b>%s</b>\n└ %s", esc(e.MonitorName), esc(meta))
	default:
		meta := e.Cause
		if e.StartedAt != nil {
			meta += " · since " + shortTime(*e.StartedAt)
		}
		fmt.Fprintf(&b, "<b>%s</b>\n└ %s", esc(e.MonitorName), esc(meta))
	}
	if e.Target != "" {
		b.WriteString("\n<code>" + esc(e.Target) + "</code>")
	}
	if len(e.RegionsDown) > 0 && !resolved && e.Kind == EventDown {
		b.WriteString("\nConfirmed from " + esc(strings.Join(e.RegionsDown, ", ")))
	}
	return b.String()
}

func (t Telegram) Send(ctx context.Context, e Event) (Delivery, error) {
	var keyboard any
	if e.DashboardURL != "" {
		keyboard = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Open monitor", "url": e.DashboardURL}}}}
	}
	original, _ := strconv.ParseInt(e.ReplyTo, 10, 64)
	if e.Kind == EventUp && original != 0 {
		edit := map[string]any{"chat_id": t.ChatID, "message_id": original, "text": telegramText(e, true), "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
		if keyboard != nil {
			edit["reply_markup"] = keyboard
		}
		_, _ = t.call(ctx, "editMessageText", edit)
		_, _ = t.call(ctx, "unpinChatMessage", map[string]any{"chat_id": t.ChatID, "message_id": original})
	}
	payload := map[string]any{
		"chat_id":              t.ChatID,
		"text":                 telegramText(e, false),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"disable_notification": e.Kind != EventDown,
	}
	if e.Kind == EventUp && original != 0 {
		payload["text"] = "🟢 <b>" + html.EscapeString(e.Headline()) + "</b>"
		payload["reply_parameters"] = map[string]any{"message_id": original, "allow_sending_without_reply": true}
	} else if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	if t.TopicID != 0 {
		payload["message_thread_id"] = t.TopicID
	}
	reply, err := t.call(ctx, "sendMessage", payload)
	if err != nil {
		return Delivery{}, err
	}
	if e.Kind == EventDown {
		_, _ = t.call(ctx, "pinChatMessage", map[string]any{"chat_id": t.ChatID, "message_id": reply.Result.MessageID, "disable_notification": true})
	}
	return Delivery{MessageID: strconv.FormatInt(reply.Result.MessageID, 10)}, nil
}

type Slack struct {
	WebhookURL string
	Client     *http.Client
}

func (s Slack) Send(ctx context.Context, e Event) (Delivery, error) {
	text := "*" + e.Headline() + "*\n" + e.Detail()
	if e.Target != "" {
		text += "\n`" + e.Target + "`"
	}
	blocks := []map[string]any{{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": text}}}
	if e.DashboardURL != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []map[string]any{{
			"type": "button", "text": map[string]string{"type": "plain_text", "text": "Open monitor"}, "url": e.DashboardURL,
		}}})
	}
	raw, err := json.Marshal(map[string]any{"text": e.Headline(), "blocks": blocks})
	if err != nil {
		return Delivery{}, err
	}
	if _, err = post(ctx, s.Client, s.WebhookURL, raw, nil); err != nil {
		return Delivery{}, fmt.Errorf("slack %w", err)
	}
	return Delivery{}, nil
}

type Webhook struct {
	URL    string
	Secret string
	Client *http.Client
	Now    func() time.Time
}

func Sign(secret string, at time.Time, body []byte) string {
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "."))
	mac.Write(body)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (h Webhook) Send(ctx context.Context, e Event) (Delivery, error) {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	raw, err := json.Marshal(map[string]any{"type": "monitor." + e.Kind, "headline": e.Headline(), "data": e})
	if err != nil {
		return Delivery{}, err
	}
	if _, err = post(ctx, h.Client, h.URL, raw, map[string]string{"X-Noria-Signature": Sign(h.Secret, now(), raw), "X-Noria-Event": "monitor." + e.Kind}); err != nil {
		return Delivery{}, fmt.Errorf("webhook %w", err)
	}
	return Delivery{}, nil
}
