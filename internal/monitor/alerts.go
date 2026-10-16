package monitor

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"
)

type Incident struct {
	Key           string `json:"key"`
	Ref           string `json:"ref"`
	Level         string `json:"level"`
	Text          string `json:"text"`
	State         string `json:"state"`
	FirstSeen     int64  `json:"firstSeen"`
	LastSeen      int64  `json:"lastSeen"`
	FiredAt       int64  `json:"firedAt,omitzero"`
	ResolvedAt    int64  `json:"resolvedAt,omitzero"`
	Notified      bool   `json:"notified"`
	NotifiedLevel string `json:"notifiedLevel,omitempty"`
	LastNotified  int64  `json:"lastNotified,omitzero"`
	Silenced      bool   `json:"silenced"`
}

type Mute struct {
	Key   string `json:"key"`
	Text  string `json:"text"`
	Since int64  `json:"since"`
	Until int64  `json:"until"`
}

type Delivery struct {
	Time    int64  `json:"time"`
	Channel string `json:"channel"`
	Event   string `json:"event"`
	Summary string `json:"summary"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

type alertState struct {
	Queue       []QueuedEvent        `json:"queue"`
	QueuedAt    int64                `json:"queuedAt"`
	Open        map[string]*Incident `json:"open"`
	History     []Incident           `json:"history"`
	Mutes       map[string]Mute      `json:"mutes"`
	Maintenance int64                `json:"maintenance"`
	Deliveries  []Delivery           `json:"deliveries"`
}

type Notification struct {
	Event     string
	Host      string
	PublicURL string
	Time      time.Time
	Incidents []Incident
	Open      []Incident
}

type Notifier interface {
	Name() string
	Level() string
	Send(context.Context, Notification) error
}

type AlertConfig struct {
	Host         string
	PublicURL    string
	DelayCrit    time.Duration
	DelayWarn    time.Duration
	ResolveAfter time.Duration
	Repeat       time.Duration
	Grace        time.Duration
	Group        time.Duration
	StateFile    string
	Channels     []Notifier
	Problems     []string
	Heartbeat    *Heartbeat
}

type QueuedEvent struct {
	Event    string   `json:"event"`
	Incident Incident `json:"incident"`
}

type Alerter struct {
	cfg      AlertConfig
	mu       sync.Mutex
	state    alertState
	started  time.Time
	outbox   []func()
	enqueued bool
	dirty    bool
	savedAt  time.Time
	failing  map[string]string
	async    func(func())
	retries  []time.Duration
}

const (
	historyKeep    = 30 * 24 * time.Hour
	historyLimit   = 500
	deliveryLimit  = 200
	eventFiring    = "firing"
	eventEscalated = "escalated"
	eventReminder  = "reminder"
	eventResolved  = "resolved"
	eventTest      = "test"
)

func NewAlerter(cfg AlertConfig, now time.Time) *Alerter {
	a := &Alerter{
		cfg:     cfg,
		started: now,
		failing: map[string]string{},
		async:   func(f func()) { go f() },
		retries: []time.Duration{0, 20 * time.Second, time.Minute},
		state:   alertState{Open: map[string]*Incident{}, Mutes: map[string]Mute{}},
	}
	if b, err := os.ReadFile(cfg.StateFile); err == nil {
		var saved alertState
		if json.Unmarshal(b, &saved) == nil {
			a.state = saved
			if a.state.Open == nil {
				a.state.Open = map[string]*Incident{}
			}
			if a.state.Mutes == nil {
				a.state.Mutes = map[string]Mute{}
			}
			for _, inc := range a.state.Open {
				inc.LastSeen = max(inc.LastSeen, now.UnixMilli())
			}
			slog.Info("alerts restored", "open", len(a.state.Open), "history", len(a.state.History))
		}
	}
	return a
}

func (a *Alerter) delay(level string) time.Duration {
	if level == levelCrit {
		return a.cfg.DelayCrit
	}
	return a.cfg.DelayWarn
}

func (a *Alerter) silenced(key string, t int64) bool {
	if a.state.Maintenance > t {
		return true
	}
	m, ok := a.state.Mutes[key]
	return ok && (m.Until == 0 || m.Until > t)
}

func (a *Alerter) Observe(now time.Time, reasons []Reason) {
	a.mu.Lock()
	a.observe(now, reasons)
	outbox := a.outbox
	a.outbox = nil
	a.mu.Unlock()
	for _, send := range outbox {
		a.async(send)
	}
}

func (a *Alerter) observe(now time.Time, reasons []Reason) {
	t := now.UnixMilli()
	for key, m := range a.state.Mutes {
		if m.Until != 0 && m.Until <= t {
			delete(a.state.Mutes, key)
			a.dirty = true
		}
	}
	if a.state.Maintenance != 0 && a.state.Maintenance <= t {
		a.state.Maintenance = 0
		a.dirty = true
	}
	warmedUp := now.Sub(a.started) >= a.cfg.Grace
	seen := map[string]bool{}
	for _, r := range reasons {
		if r.Key == "" || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		inc := a.state.Open[r.Key]
		if inc == nil {
			inc = &Incident{Key: r.Key, State: "pending", FirstSeen: t}
			a.state.Open[r.Key] = inc
			a.dirty = true
		}
		if inc.Text != r.Text || inc.Level != r.Level {
			a.dirty = true
		}
		inc.Ref, inc.Text, inc.Level, inc.LastSeen = r.Ref, r.Text, r.Level, t
		switch {
		case inc.State == "pending":
			if warmedUp && t-inc.FirstSeen >= a.delay(r.Level).Milliseconds() {
				inc.State, inc.FiredAt = "firing", t
				a.fire(inc, eventFiring, t)
			}
		case inc.Silenced && !a.silenced(inc.Key, t):
			a.fire(inc, eventFiring, t)
		case inc.Notified && levelRank[inc.Level] > levelRank[inc.NotifiedLevel]:
			a.fire(inc, eventEscalated, t)
		case inc.Notified && inc.Level == levelCrit && a.cfg.Repeat > 0 && t-inc.LastNotified >= a.cfg.Repeat.Milliseconds():
			a.fire(inc, eventReminder, t)
		}
	}
	for key, inc := range a.state.Open {
		if seen[key] {
			continue
		}
		if inc.State == "pending" {
			delete(a.state.Open, key)
			a.dirty = true
			continue
		}
		if !warmedUp || t-inc.LastSeen < a.cfg.ResolveAfter.Milliseconds() {
			continue
		}
		inc.State, inc.ResolvedAt = "resolved", t
		delete(a.state.Open, key)
		if m, ok := a.state.Mutes[key]; ok && m.Until == 0 {
			delete(a.state.Mutes, key)
		}
		a.state.History = append(a.state.History, *inc)
		if inc.Notified {
			a.enqueue(eventResolved, *inc, now)
		}
		a.dirty = true
	}
	a.trimHistory(t)
	a.flush(now)
	if a.enqueued || (a.dirty && now.Sub(a.savedAt) >= 5*time.Second) {
		a.enqueued = false
		a.saveLocked(now)
	}
}

func (a *Alerter) fire(inc *Incident, event string, t int64) {
	a.dirty = true
	if a.silenced(inc.Key, t) {
		if event == eventFiring {
			inc.Silenced = true
		}
		return
	}
	inc.Silenced = false
	inc.Notified = true
	inc.NotifiedLevel = inc.Level
	inc.LastNotified = t
	a.enqueue(event, *inc, time.UnixMilli(t))
}

func (a *Alerter) enqueue(event string, inc Incident, now time.Time) {
	if len(a.state.Queue) == 0 {
		a.state.QueuedAt = now.UnixMilli()
	}
	a.state.Queue = append(a.state.Queue, QueuedEvent{event, inc})
	a.enqueued, a.dirty = true, true
}

func (a *Alerter) flush(now time.Time) {
	if len(a.state.Queue) == 0 || now.Sub(time.UnixMilli(a.state.QueuedAt)) < a.cfg.Group {
		return
	}
	var order []string
	byEvent := map[string][]Incident{}
	t := now.UnixMilli()
	for _, q := range a.state.Queue {
		if q.Event != eventResolved && a.silenced(q.Incident.Key, t) {
			continue
		}
		if byEvent[q.Event] == nil {
			order = append(order, q.Event)
		}
		byEvent[q.Event] = append(byEvent[q.Event], q.Incident)
	}
	a.state.Queue = nil
	a.dirty = true
	open := a.openFiring()
	for _, event := range order {
		a.dispatch(Notification{Event: event, Host: a.cfg.Host, PublicURL: a.cfg.PublicURL, Time: now, Incidents: byEvent[event], Open: open})
	}
}

func (a *Alerter) openFiring() []Incident {
	var open []Incident
	for _, inc := range a.state.Open {
		if inc.State == "firing" {
			open = append(open, *inc)
		}
	}
	slices.SortFunc(open, func(x, y Incident) int {
		return cmp.Or(cmp.Compare(levelRank[y.Level], levelRank[x.Level]), cmp.Compare(x.FirstSeen, y.FirstSeen))
	})
	return open
}

func (a *Alerter) dispatch(n Notification) {
	for _, ch := range a.cfg.Channels {
		var picked []Incident
		for _, inc := range n.Incidents {
			level := inc.Level
			if n.Event == eventResolved {
				level = inc.NotifiedLevel
			}
			if levelRank[level] >= levelRank[ch.Level()] {
				picked = append(picked, inc)
			}
		}
		if len(picked) == 0 {
			continue
		}
		msg := n
		msg.Incidents = picked
		a.outbox = append(a.outbox, func() { a.deliver(ch, msg) })
	}
}

func (a *Alerter) deliver(ch Notifier, n Notification) {
	var err error
	for _, wait := range a.retries {
		time.Sleep(wait)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = ch.Send(ctx, n)
		cancel()
		if err == nil {
			break
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := Delivery{Time: time.Now().UnixMilli(), Channel: ch.Name(), Event: n.Event, Summary: summary(n), OK: err == nil}
	if err != nil {
		d.Error = err.Error()
		a.failing[ch.Name()] = d.Error
		slog.Warn("alert not delivered", "channel", ch.Name(), "err", err)
	} else {
		delete(a.failing, ch.Name())
	}
	a.state.Deliveries = append(a.state.Deliveries, d)
	if len(a.state.Deliveries) > deliveryLimit {
		a.state.Deliveries = a.state.Deliveries[len(a.state.Deliveries)-deliveryLimit:]
	}
	a.dirty = true
}

func (a *Alerter) trimHistory(t int64) {
	cutoff := t - historyKeep.Milliseconds()
	start := 0
	for start < len(a.state.History) && a.state.History[start].ResolvedAt < cutoff {
		start++
	}
	if over := len(a.state.History) - start - historyLimit; over > 0 {
		start += over
	}
	if start > 0 {
		a.state.History = slices.Clone(a.state.History[start:])
		a.dirty = true
	}
}

func (a *Alerter) Findings() []Reason {
	a.mu.Lock()
	var out []Reason
	for _, name := range slicesSortedKeys(a.failing) {
		out = append(out, Reason{Level: levelWarn, Text: fmt.Sprintf("Alerts to %s are failing: %s", name, a.failing[name]), Ref: "alerts", Key: "alerts/failing:" + name})
	}
	for i, p := range a.cfg.Problems {
		out = append(out, Reason{Level: levelWarn, Text: p, Ref: "alerts", Key: fmt.Sprintf("alerts/config:%d", i)})
	}
	a.mu.Unlock()
	if hb := a.cfg.Heartbeat; hb != nil {
		if problem := hb.Problem(); problem != "" {
			out = append(out, Reason{Level: levelWarn, Text: problem, Ref: "alerts", Key: "alerts/heartbeat"})
		}
	}
	return out
}

func slicesSortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

type AlertSummary struct {
	Configured  bool     `json:"configured"`
	Maintenance int64    `json:"maintenance"`
	Muted       []string `json:"muted"`
	Firing      int      `json:"firing"`
}

func (a *Alerter) Summary() AlertSummary {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := AlertSummary{Configured: len(a.cfg.Channels) > 0, Maintenance: a.state.Maintenance, Muted: []string{}}
	for key := range a.state.Mutes {
		s.Muted = append(s.Muted, key)
	}
	slices.Sort(s.Muted)
	for _, inc := range a.state.Open {
		if inc.State == "firing" {
			s.Firing++
		}
	}
	return s
}

type ChannelStatus struct {
	Name    string `json:"name"`
	Level   string `json:"level"`
	Failing string `json:"failing,omitempty"`
}

type AlertStatus struct {
	Channels    []ChannelStatus  `json:"channels"`
	Problems    []string         `json:"problems"`
	Heartbeat   *HeartbeatStatus `json:"heartbeat"`
	PublicURL   string           `json:"publicUrl"`
	Maintenance int64            `json:"maintenance"`
	Settings    []Setting        `json:"settings"`
	Open        []Incident       `json:"open"`
	History     []Incident       `json:"history"`
	Mutes       []Mute           `json:"mutes"`
	Deliveries  []Delivery       `json:"deliveries"`
}

func (a *Alerter) Status() AlertStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := AlertStatus{
		Channels:    []ChannelStatus{},
		Problems:    append([]string{}, a.cfg.Problems...),
		PublicURL:   a.cfg.PublicURL,
		Maintenance: a.state.Maintenance,
		Open:        []Incident{},
		History:     []Incident{},
		Mutes:       []Mute{},
		Deliveries:  []Delivery{},
		Settings: []Setting{
			{Name: "Critical alerts after", Value: humanDuration(a.cfg.DelayCrit.Seconds())},
			{Name: "Warning alerts after", Value: humanDuration(a.cfg.DelayWarn.Seconds())},
			{Name: "Resolved after clear for", Value: humanDuration(a.cfg.ResolveAfter.Seconds())},
			{Name: "Critical reminders every", Value: repeatLabel(a.cfg.Repeat)},
			{Name: "Grouped within", Value: humanDuration(a.cfg.Group.Seconds())},
		},
	}
	for _, ch := range a.cfg.Channels {
		s.Channels = append(s.Channels, ChannelStatus{Name: ch.Name(), Level: ch.Level(), Failing: a.failing[ch.Name()]})
	}
	if a.cfg.Heartbeat != nil {
		hs := a.cfg.Heartbeat.Status()
		s.Heartbeat = &hs
	}
	for _, inc := range a.state.Open {
		s.Open = append(s.Open, *inc)
	}
	slices.SortFunc(s.Open, func(x, y Incident) int {
		return cmp.Or(cmp.Compare(stateRank(x.State), stateRank(y.State)), cmp.Compare(levelRank[y.Level], levelRank[x.Level]), cmp.Compare(x.FirstSeen, y.FirstSeen))
	})
	for i := len(a.state.History) - 1; i >= 0 && len(s.History) < 100; i-- {
		s.History = append(s.History, a.state.History[i])
	}
	for _, m := range a.state.Mutes {
		s.Mutes = append(s.Mutes, m)
	}
	slices.SortFunc(s.Mutes, func(x, y Mute) int { return cmp.Compare(x.Since, y.Since) })
	for i := len(a.state.Deliveries) - 1; i >= 0 && len(s.Deliveries) < 100; i-- {
		s.Deliveries = append(s.Deliveries, a.state.Deliveries[i])
	}
	return s
}

func stateRank(state string) int {
	if state == "firing" {
		return 0
	}
	return 1
}

func repeatLabel(d time.Duration) string {
	if d <= 0 {
		return "never"
	}
	return humanDuration(d.Seconds())
}

func (a *Alerter) Mute(key string, minutes int, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := Mute{Key: key, Since: now.UnixMilli()}
	if inc, ok := a.state.Open[key]; ok {
		m.Text = inc.Text
	} else {
		return errors.New("no open problem with that key")
	}
	if minutes > 0 {
		m.Until = now.Add(time.Duration(minutes) * time.Minute).UnixMilli()
	}
	a.state.Mutes[key] = m
	a.dirty = true
	return nil
}

func (a *Alerter) Unmute(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.state.Mutes, key)
	a.dirty = true
}

func (a *Alerter) SetMaintenance(minutes int, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Maintenance = 0
	if minutes > 0 {
		a.state.Maintenance = now.Add(time.Duration(minutes) * time.Minute).UnixMilli()
	}
	a.dirty = true
}

func (a *Alerter) Test(ctx context.Context, channel string, now time.Time) error {
	var ch Notifier
	for _, c := range a.cfg.Channels {
		if strings.EqualFold(c.Name(), channel) {
			ch = c
		}
	}
	if ch == nil {
		return fmt.Errorf("no %s channel is configured", channel)
	}
	a.mu.Lock()
	n := Notification{Event: eventTest, Host: a.cfg.Host, PublicURL: a.cfg.PublicURL, Time: now, Open: a.openFiring()}
	a.mu.Unlock()
	err := ch.Send(ctx, n)
	a.mu.Lock()
	defer a.mu.Unlock()
	d := Delivery{Time: now.UnixMilli(), Channel: ch.Name(), Event: eventTest, Summary: "Test alert", OK: err == nil}
	if err != nil {
		d.Error = err.Error()
	} else {
		delete(a.failing, ch.Name())
	}
	a.state.Deliveries = append(a.state.Deliveries, d)
	a.dirty = true
	return err
}

func (a *Alerter) Save() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saveLocked(time.Now())
}

func (a *Alerter) saveLocked(now time.Time) {
	a.savedAt, a.dirty = now, false
	if a.cfg.StateFile == "" {
		return
	}
	if err := writeJSONAtomic(a.cfg.StateFile, a.state); err != nil {
		slog.Warn("alerts not saved", "err", err)
	}
}

func refLink(base, ref string) string {
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	kind, name, _ := strings.Cut(ref, ":")
	switch {
	case kind == "container" && name != "":
		return base + "/#/containers/" + url.PathEscape(name)
	case name != "":
		return base + "/#/services/" + kind + "/" + url.PathEscape(name)
	case ref == "alerts":
		return base + "/#/alerts"
	}
	return base + "/#/"
}

var eventHeadings = map[string]string{
	eventFiring:    "",
	eventEscalated: "now critical",
	eventReminder:  "still failing",
	eventResolved:  "resolved",
	eventTest:      "test",
}

func levelWord(level string) string {
	if level == levelCrit {
		return "critical"
	}
	return "warning"
}

func ago(from int64, now time.Time) string {
	return humanDuration(now.Sub(time.UnixMilli(from)).Seconds())
}

func summary(n Notification) string {
	if n.Event == eventTest {
		return "Test alert"
	}
	if len(n.Incidents) == 1 {
		return n.Incidents[0].Text
	}
	return fmt.Sprintf("%d problems", len(n.Incidents))
}

func worstLevel(incidents []Incident) string {
	level := levelWarn
	for _, inc := range incidents {
		if inc.Level == levelCrit || inc.NotifiedLevel == levelCrit {
			level = levelCrit
		}
	}
	return level
}

func subject(n Notification) string {
	tag := map[string]string{eventEscalated: "CRITICAL", eventReminder: "STILL FAILING", eventResolved: "RESOLVED", eventTest: "TEST"}[n.Event]
	if tag == "" {
		tag = strings.ToUpper(levelWord(worstLevel(n.Incidents)))
	}
	text := summary(n)
	switch {
	case n.Event == eventTest:
		text = "alerts are working"
	case n.Event == eventResolved && len(n.Incidents) == 1:
		text = fmt.Sprintf("%s (lasted %s)", n.Incidents[0].Text, humanDuration(float64(n.Incidents[0].ResolvedAt-n.Incidents[0].FirstSeen)/1000))
	}
	return fmt.Sprintf("[%s] %s: %s", tag, n.Host, text)
}

func incidentLine(n Notification, inc Incident) string {
	if n.Event == eventResolved {
		return fmt.Sprintf("%s · lasted %s", inc.Text, humanDuration(float64(inc.ResolvedAt-inc.FirstSeen)/1000))
	}
	return fmt.Sprintf("%s · %s for %s", inc.Text, levelWord(inc.Level), ago(inc.FirstSeen, n.Time))
}

type TelegramNotifier struct {
	token, chat, level, api string
	client                  *http.Client
}

func NewTelegramNotifier(token, chat, level string) *TelegramNotifier {
	return &TelegramNotifier{token: token, chat: chat, level: level, api: "https://api.telegram.org", client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *TelegramNotifier) Name() string  { return "Telegram" }
func (t *TelegramNotifier) Level() string { return t.level }

func (t *TelegramNotifier) Send(ctx context.Context, n Notification) error {
	body, _ := json.Marshal(map[string]any{"chat_id": t.chat, "text": telegramText(n), "parse_mode": "HTML", "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.api+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: invalid API address")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return fmt.Errorf("telegram: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.UnmarshalRead(resp.Body, &result, lenient)
	if !result.OK {
		return fmt.Errorf("telegram: %s", cmp.Or(result.Description, resp.Status))
	}
	return nil
}

func telegramText(n Notification) string {
	esc := html.EscapeString
	icon := map[string]string{eventResolved: "✅", eventTest: "🧪", eventReminder: "🔁"}[n.Event]
	if icon == "" {
		icon = map[string]string{levelCrit: "🔴", levelWarn: "🟠"}[worstLevel(n.Incidents)]
	}
	heading := eventHeadings[n.Event]
	if heading == "" {
		heading = levelWord(worstLevel(n.Incidents))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> · %s\n", icon, esc(n.Host), heading)
	if n.Event == eventTest {
		b.WriteString("Alerts from the monitor reach this chat.\n")
		if len(n.Open) > 0 {
			fmt.Fprintf(&b, "%d %s open right now.\n", len(n.Open), pluralWord(len(n.Open), "problem is", "problems are"))
		}
	}
	for _, inc := range n.Incidents {
		line := esc(incidentLine(n, inc))
		if link := refLink(n.PublicURL, inc.Ref); link != "" && len(n.Incidents) == 1 {
			line += fmt.Sprintf(` · <a href="%s">open</a>`, esc(link))
		}
		if len(n.Incidents) > 1 {
			line = "• " + line
		}
		b.WriteString(line + "\n")
	}
	if n.PublicURL != "" && (len(n.Incidents) != 1 || n.Event == eventTest) {
		fmt.Fprintf(&b, `<a href="%s">Open the dashboard</a>`, esc(strings.TrimRight(n.PublicURL, "/")+"/#/"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

type EmailNotifier struct {
	server   *url.URL
	from     string
	to       []string
	level    string
	insecure bool
}

func NewEmailNotifier(rawURL, from, to, level string) (*EmailNotifier, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "smtp" && u.Scheme != "smtps") || u.Hostname() == "" {
		return nil, errors.New("ALERT_SMTP_URL must look like smtp://user:password@host:587 or smtps://user:password@host:465")
	}
	var recipients []string
	for r := range strings.SplitSeq(to, ",") {
		if r = strings.TrimSpace(r); r != "" {
			recipients = append(recipients, r)
		}
	}
	if from == "" || len(recipients) == 0 {
		return nil, errors.New("email alerts need ALERT_EMAIL_FROM and ALERT_EMAIL_TO")
	}
	return &EmailNotifier{server: u, from: from, to: recipients, level: level, insecure: u.Query().Get("tls") == "none"}, nil
}

func (e *EmailNotifier) Name() string  { return "Email" }
func (e *EmailNotifier) Level() string { return e.level }

func (e *EmailNotifier) Send(ctx context.Context, n Notification) error {
	host := e.server.Hostname()
	port := e.server.Port()
	if port == "" {
		port = map[string]string{"smtp": "587", "smtps": "465"}[e.server.Scheme]
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if e.server.Scheme == "smtps" {
		conn = tls.Client(conn, &tls.Config{ServerName: host})
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	defer c.Close()
	if e.server.Scheme == "smtp" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("email: %w", err)
			}
		} else if !e.insecure {
			return errors.New("email: the server does not offer STARTTLS; use smtps:// or add ?tls=none for a trusted local relay")
		}
	}
	if u := e.server.User; u != nil {
		password, _ := u.Password()
		if err := c.Auth(smtp.PlainAuth("", u.Username(), password, host)); err != nil {
			return fmt.Errorf("email: %w", err)
		}
	}
	if err := c.Mail(e.from); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	for _, r := range e.to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("email: %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if _, err := w.Write(emailMessage(e.from, e.to, n)); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: %w", err)
	}
	return c.Quit()
}

func messageIDHost(host string) string {
	if valid := strings.Map(func(r rune) rune {
		if r < 128 && (r == '-' || r == '.' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return r
		}
		return -1
	}, host); valid != "" {
		return valid
	}
	return "monitor"
}

func emailMessage(from string, to []string, n Notification) []byte {
	var body strings.Builder
	switch n.Event {
	case eventTest:
		body.WriteString("This is a test. Alerts from the monitor reach this address.\n")
	case eventResolved:
		body.WriteString("Resolved:\n")
	case eventEscalated:
		body.WriteString("Now critical:\n")
	case eventReminder:
		body.WriteString("Still failing:\n")
	default:
		body.WriteString("Problems:\n")
	}
	for _, inc := range n.Incidents {
		fmt.Fprintf(&body, "\n  %s\n", incidentLine(n, inc))
		if link := refLink(n.PublicURL, inc.Ref); link != "" {
			fmt.Fprintf(&body, "  %s\n", link)
		}
	}
	shown := map[string]bool{}
	for _, inc := range n.Incidents {
		shown[inc.Key] = true
	}
	var others []Incident
	for _, inc := range n.Open {
		if !shown[inc.Key] {
			others = append(others, inc)
		}
	}
	if len(others) > 0 {
		body.WriteString("\nAlso open:\n")
		for _, inc := range others {
			fmt.Fprintf(&body, "  %s\n", incidentLine(Notification{Time: n.Time}, inc))
		}
	}
	if n.PublicURL != "" {
		fmt.Fprintf(&body, "\nDashboard: %s/#/\nMute alerts or pause them for maintenance: %s/#/alerts\n", strings.TrimRight(n.PublicURL, "/"), strings.TrimRight(n.PublicURL, "/"))
	}
	fmt.Fprintf(&body, "\nSent by the monitor on %s.\n", n.Host)

	var m strings.Builder
	fmt.Fprintf(&m, "From: %s\r\n", from)
	fmt.Fprintf(&m, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&m, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject(n)))
	fmt.Fprintf(&m, "Date: %s\r\n", n.Time.Format(time.RFC1123Z))
	fmt.Fprintf(&m, "Message-ID: <%s@%s>\r\n", uuid.NewV7(), messageIDHost(n.Host))
	m.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	m.WriteString(strings.ReplaceAll(body.String(), "\n", "\r\n"))
	return []byte(m.String())
}

type HeartbeatStatus struct {
	Target string `json:"target"`
	LastAt int64  `json:"lastAt"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Fails  int    `json:"fails"`
}

type Heartbeat struct {
	url    string
	target string
	client *http.Client
	mu     sync.Mutex
	status HeartbeatStatus
}

func NewHeartbeat(raw string) (*Heartbeat, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("HEARTBEAT_URL must be an http:// or https:// address")
	}
	return &Heartbeat{url: raw, target: u.Host, client: &http.Client{Timeout: 10 * time.Second}, status: HeartbeatStatus{Target: u.Host}}, nil
}

func (h *Heartbeat) Run(ctx context.Context, every time.Duration) {
	h.ping(ctx)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.ping(ctx)
		}
	}
}

func (h *Heartbeat) ping(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	resp, err := h.client.Do(req)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.LastAt = time.Now().UnixMilli()
	switch {
	case err != nil:
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		h.status.OK, h.status.Error = false, friendlyError(err)
	case resp.StatusCode >= 300:
		h.status.OK, h.status.Error = false, fmt.Sprintf("answered HTTP %d", resp.StatusCode)
	default:
		h.status.OK, h.status.Error = true, ""
	}
	if resp != nil {
		resp.Body.Close()
	}
	if h.status.OK {
		h.status.Fails = 0
	} else {
		h.status.Fails++
	}
}

func (h *Heartbeat) Status() HeartbeatStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

func (h *Heartbeat) Problem() string {
	s := h.Status()
	if s.Fails < 3 {
		return ""
	}
	return fmt.Sprintf("Heartbeat to %s is failing: %s", s.Target, s.Error)
}

func alertConfigFromEnv(host string) AlertConfig {
	cfg := AlertConfig{
		Host:      host,
		PublicURL: strings.TrimRight(os.Getenv("MONITOR_PUBLIC_URL"), "/"),
		StateFile: env("ALERTS_FILE", "/data/alerts.json"),
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		raw := os.Getenv(key)
		if raw == "" {
			return fallback
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s=%q is not a duration such as 90s, 5m, or 4h; using %s", key, raw, fallback))
			return fallback
		}
		return d
	}
	level := func(key, fallback string) string {
		switch v := strings.ToLower(os.Getenv(key)); v {
		case "":
			return fallback
		case "crit", "critical":
			return levelCrit
		case "warn", "warning":
			return levelWarn
		default:
			cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s=%q must be warning or critical; using %s", key, v, levelWord(fallback)))
			return fallback
		}
	}
	cfg.DelayCrit = duration("ALERT_DELAY_CRITICAL", time.Minute)
	cfg.DelayWarn = duration("ALERT_DELAY_WARNING", 5*time.Minute)
	cfg.ResolveAfter = duration("ALERT_RESOLVE_AFTER", 2*time.Minute)
	cfg.Repeat = duration("ALERT_REPEAT", 4*time.Hour)
	cfg.Group = duration("ALERT_GROUP_WINDOW", 30*time.Second)
	cfg.Grace = duration("ALERT_STARTUP_GRACE", 2*time.Minute)
	token, chat := os.Getenv("ALERT_TELEGRAM_TOKEN"), os.Getenv("ALERT_TELEGRAM_CHAT")
	switch {
	case token != "" && chat != "":
		tg := NewTelegramNotifier(token, chat, level("ALERT_TELEGRAM_LEVEL", levelWarn))
		if api := os.Getenv("ALERT_TELEGRAM_API"); api != "" {
			tg.api = strings.TrimRight(api, "/")
		}
		cfg.Channels = append(cfg.Channels, tg)
	case token != "" || chat != "":
		cfg.Problems = append(cfg.Problems, "Telegram alerts need both ALERT_TELEGRAM_TOKEN and ALERT_TELEGRAM_CHAT")
	}
	if raw := os.Getenv("ALERT_SMTP_URL"); raw != "" {
		email, err := NewEmailNotifier(raw, os.Getenv("ALERT_EMAIL_FROM"), os.Getenv("ALERT_EMAIL_TO"), level("ALERT_EMAIL_LEVEL", levelCrit))
		if err != nil {
			cfg.Problems = append(cfg.Problems, "Email alerts are off: "+err.Error())
		} else {
			cfg.Channels = append(cfg.Channels, email)
		}
	}
	if raw := os.Getenv("HEARTBEAT_URL"); raw != "" {
		hb, err := NewHeartbeat(raw)
		if err != nil {
			cfg.Problems = append(cfg.Problems, err.Error())
		} else {
			cfg.Heartbeat = hb
		}
	}
	if len(cfg.Channels) > 0 && cfg.PublicURL == "" {
		cfg.Problems = append(cfg.Problems, "Set MONITOR_PUBLIC_URL so alerts can link to the dashboard")
	}
	return cfg
}

func (v *Verdict) include(reasons []Reason) {
	for _, r := range reasons {
		v.Reasons = append(v.Reasons, r)
		if r.Level == levelCrit || (r.Level == levelWarn && v.Level == levelOK) {
			v.Level = r.Level
		}
	}
}
