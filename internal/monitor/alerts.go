package monitor

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"uuid"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/credentials/endpointcreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	"github.com/thekiharani/monitor/templates"
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
	Via     string `json:"via,omitempty"`
	Note    string `json:"note,omitempty"`
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
		cs := ChannelStatus{Name: ch.Name(), Level: ch.Level(), Failing: a.failing[ch.Name()]}
		if described, ok := ch.(interface{ Via() string }); ok {
			cs.Via = described.Via()
		}
		if noted, ok := ch.(interface{ Note() string }); ok {
			cs.Note = noted.Note()
		}
		s.Channels = append(s.Channels, cs)
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
	topic                   int
	quietFrom, quietTo      int
	client                  *http.Client
	stateFile               string

	mu    sync.Mutex
	state telegramState
	note  string
}

type telegramPost struct {
	ID      int              `json:"id"`
	Notice  Notification     `json:"notice"`
	Cleared map[string]int64 `json:"cleared,omitempty"`
	Pinned  bool             `json:"pinned,omitzero"`
}

type telegramState struct {
	Chat    string          `json:"chat"`
	MovedTo string          `json:"movedTo,omitempty"`
	Posts   []*telegramPost `json:"posts"`
}

type telegramError struct {
	Description string
	MigrateTo   int64
}

func (e *telegramError) Error() string { return "telegram: " + e.Description }

type telegramButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

func NewTelegramNotifier(token, chat, level string) *TelegramNotifier {
	return &TelegramNotifier{token: token, chat: chat, level: level, api: "https://api.telegram.org", quietFrom: -1, quietTo: -1,
		client: &http.Client{Timeout: 15 * time.Second}, state: telegramState{Chat: chat}}
}

func (t *TelegramNotifier) Name() string  { return "Telegram" }
func (t *TelegramNotifier) Level() string { return t.level }

func (t *TelegramNotifier) Via() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	via := "chat " + t.chat
	if t.topic > 0 {
		via += fmt.Sprintf(", topic %d", t.topic)
	}
	if t.quietFrom >= 0 {
		via += fmt.Sprintf(", warnings silent %02d:%02d to %02d:%02d", t.quietFrom/60, t.quietFrom%60, t.quietTo/60, t.quietTo%60)
	}
	return via
}

func (t *TelegramNotifier) Note() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.note
}

func parseQuietHours(raw string) (from, to int, err error) {
	start, end, ok := strings.Cut(raw, "-")
	clock := func(s string) (int, error) {
		c, err := time.Parse("15:04", strings.TrimSpace(s))
		return c.Hour()*60 + c.Minute(), err
	}
	if !ok {
		return -1, -1, fmt.Errorf("TELEGRAM_QUIET_HOURS=%q must look like 22:00-07:00", raw)
	}
	if from, err = clock(start); err == nil {
		to, err = clock(end)
	}
	if err != nil || from == to {
		return -1, -1, fmt.Errorf("TELEGRAM_QUIET_HOURS=%q must look like 22:00-07:00", raw)
	}
	return from, to, nil
}

func (t *TelegramNotifier) quiet(at time.Time) bool {
	if t.quietFrom < 0 {
		return false
	}
	m := at.Hour()*60 + at.Minute()
	if t.quietFrom < t.quietTo {
		return m >= t.quietFrom && m < t.quietTo
	}
	return m >= t.quietFrom || m < t.quietTo
}

func (t *TelegramNotifier) load() {
	if t.stateFile == "" {
		return
	}
	b, err := os.ReadFile(t.stateFile)
	if err != nil {
		return
	}
	var saved telegramState
	if json.Unmarshal(b, &saved, lenient) != nil || saved.Chat != t.chat {
		return
	}
	t.state = saved
	if saved.MovedTo != "" {
		t.chat = saved.MovedTo
		t.note = movedNote(saved.MovedTo)
	}
}

func (t *TelegramNotifier) save() {
	if t.stateFile == "" {
		return
	}
	if err := writeJSONAtomic(t.stateFile, t.state); err != nil {
		slog.Warn("telegram state not saved", "err", err)
	}
}

func movedNote(chat string) string {
	return "The group became a supergroup, so messages now go to " + chat + ". Set TELEGRAM_CHAT_ID=" + chat + " to match."
}

func (t *TelegramNotifier) Send(ctx context.Context, n Notification) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	text, buttons := telegramMessage(n)
	payload := map[string]any{
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
		"disable_notification": n.Event == eventResolved || (n.Event != eventTest && worstLevel(n.Incidents) == levelWarn && t.quiet(n.Time)),
	}
	if t.topic > 0 {
		payload["message_thread_id"] = t.topic
	}
	if n.Event != eventFiring && n.Event != eventTest {
		if original := t.originalFor(n.Incidents); original != nil {
			payload["reply_parameters"] = map[string]any{"message_id": original.ID, "allow_sending_without_reply": true}
		}
	}
	if len(buttons) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]telegramButton{buttons}}
	}
	id, err := t.sendMessage(ctx, payload, text, buttons)
	if err != nil {
		return err
	}
	switch n.Event {
	case eventTest:
		return nil
	case eventResolved:
		t.markResolved(ctx, n)
	default:
		n.Open = nil
		post := &telegramPost{ID: id, Notice: n}
		if n.Event != eventReminder && worstLevel(n.Incidents) == levelCrit {
			t.pin(ctx, post)
		}
		t.state.Posts = append(t.state.Posts, post)
	}
	cutoff := n.Time.Add(-7 * 24 * time.Hour)
	t.state.Posts = slices.DeleteFunc(t.state.Posts, func(p *telegramPost) bool { return p.Notice.Time.Before(cutoff) })
	t.save()
	return nil
}

func (t *TelegramNotifier) sendMessage(ctx context.Context, payload map[string]any, text string, buttons []telegramButton) (int, error) {
	var sent struct {
		MessageID int `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", payload, &sent)
	if err != nil && len(buttons) > 0 && strings.Contains(strings.ToLower(err.Error()), "button") {
		links := make([]string, len(buttons))
		for i, b := range buttons {
			links[i] = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(b.URL), html.EscapeString(b.Text))
		}
		delete(payload, "reply_markup")
		payload["text"] = text + "\n\n" + strings.Join(links, "  ·  ")
		err = t.call(ctx, "sendMessage", payload, &sent)
	}
	return sent.MessageID, err
}

func (t *TelegramNotifier) originalFor(incidents []Incident) *telegramPost {
	for _, post := range t.state.Posts {
		for _, posted := range post.Notice.Incidents {
			for _, inc := range incidents {
				if posted.Key == inc.Key {
					return post
				}
			}
		}
	}
	return nil
}

func (t *TelegramNotifier) markResolved(ctx context.Context, n Notification) {
	resolved := map[string]int64{}
	for _, inc := range n.Incidents {
		resolved[inc.Key] = inc.ResolvedAt
	}
	t.state.Posts = slices.DeleteFunc(t.state.Posts, func(post *telegramPost) bool {
		changed, open := false, false
		for _, inc := range post.Notice.Incidents {
			if at, ok := resolved[inc.Key]; ok && post.Cleared[inc.Key] == 0 {
				if post.Cleared == nil {
					post.Cleared = map[string]int64{}
				}
				post.Cleared[inc.Key], changed = at, true
			}
			open = open || post.Cleared[inc.Key] == 0
		}
		if !changed {
			return false
		}
		text, buttons := telegramRender(post.Notice, post.Cleared)
		edit := map[string]any{"message_id": post.ID, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true}}
		if len(buttons) > 0 {
			edit["reply_markup"] = map[string]any{"inline_keyboard": [][]telegramButton{buttons}}
		}
		if err := t.call(ctx, "editMessageText", edit, nil); err != nil && !strings.Contains(err.Error(), "not modified") {
			slog.Warn("telegram alert not updated", "err", err)
			if strings.Contains(err.Error(), "not found") {
				return true
			}
		}
		if !open && post.Pinned {
			t.call(ctx, "unpinChatMessage", map[string]any{"message_id": post.ID}, nil)
		}
		return !open
	})
}

func (t *TelegramNotifier) pin(ctx context.Context, post *telegramPost) {
	err := t.call(ctx, "pinChatMessage", map[string]any{"message_id": post.ID, "disable_notification": true}, nil)
	post.Pinned = err == nil
	switch {
	case err == nil:
		if strings.HasPrefix(t.note, "Critical alerts aren't pinned") {
			t.note = ""
		}
	case strings.Contains(err.Error(), "rights"):
		t.note = "Critical alerts aren't pinned: make the bot an admin with the Pin messages right."
	}
}

func (t *TelegramNotifier) call(ctx context.Context, method string, payload map[string]any, result any) error {
	for attempt := 0; ; attempt++ {
		payload["chat_id"] = t.chat
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.api+"/bot"+t.token+"/"+method, bytes.NewReader(body))
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
		var reply struct {
			OK          bool           `json:"ok"`
			Description string         `json:"description"`
			Result      jsontext.Value `json:"result"`
			Parameters  struct {
				MigrateTo int64 `json:"migrate_to_chat_id"`
			} `json:"parameters"`
		}
		json.UnmarshalRead(resp.Body, &reply, lenient)
		resp.Body.Close()
		if reply.OK {
			if result != nil {
				json.Unmarshal(reply.Result, result, lenient)
			}
			return nil
		}
		if moved := reply.Parameters.MigrateTo; moved != 0 && attempt == 0 {
			t.chat = strconv.FormatInt(moved, 10)
			t.state.MovedTo, t.note = t.chat, movedNote(t.chat)
			slog.Warn("telegram group became a supergroup", "chat", t.chat)
			continue
		}
		return &telegramError{Description: cmp.Or(reply.Description, resp.Status), MigrateTo: reply.Parameters.MigrateTo}
	}
}

func shortTime(ms int64, now time.Time) string {
	t := time.UnixMilli(ms).In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

func telegramMessage(n Notification) (string, []telegramButton) {
	return telegramRender(n, nil)
}

func telegramRender(n Notification, cleared map[string]int64) (string, []telegramButton) {
	esc := html.EscapeString
	dot := map[string]string{levelCrit: "🔴", levelWarn: "🟠"}
	clearedAt := func(inc Incident) int64 {
		if n.Event == eventResolved {
			return inc.ResolvedAt
		}
		return cleared[inc.Key]
	}
	allCleared := len(n.Incidents) > 0
	for _, inc := range n.Incidents {
		allCleared = allCleared && clearedAt(inc) > 0
	}
	worst := worstLevel(n.Incidents)
	icon, label := dot[worst], strings.ToUpper(levelWord(worst))
	switch {
	case n.Event == eventTest:
		icon, label = "🧪", "TEST"
	case allCleared:
		icon, label = "✅", "RESOLVED"
	case n.Event == eventEscalated:
		icon, label = "🔺", "NOW CRITICAL"
	case n.Event == eventReminder:
		icon, label = "🔁", "STILL FAILING"
	}
	row := func(inc Incident) (mark, meta string) {
		if at := clearedAt(inc); at > 0 {
			return "✅", fmt.Sprintf("lasted %s · cleared at %s", humanDuration(float64(at-inc.FirstSeen)/1000), shortTime(at, n.Time))
		}
		return dot[inc.Level], fmt.Sprintf("%s for %s · since %s", levelWord(inc.Level), ago(inc.FirstSeen, n.Time), shortTime(inc.FirstSeen, n.Time))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>  ·  <code>%s</code>\n\n", icon, label, esc(n.Host))
	switch {
	case n.Event == eventTest:
		b.WriteString("<b>Alerts reach this chat</b>\nNothing is wrong. This is a test.")
	case len(n.Incidents) == 1:
		_, meta := row(n.Incidents[0])
		fmt.Fprintf(&b, "<b>%s</b>\n└ %s", esc(n.Incidents[0].Text), esc(meta))
	default:
		for i, inc := range n.Incidents {
			if i > 0 {
				b.WriteString("\n")
			}
			mark, meta := row(inc)
			fmt.Fprintf(&b, "%s <b>%s</b>\n└ %s", mark, esc(inc.Text), esc(meta))
		}
	}

	shown := map[string]bool{}
	for _, inc := range n.Incidents {
		shown[inc.Key] = true
	}
	var others []string
	for _, inc := range n.Open {
		if !shown[inc.Key] {
			others = append(others, fmt.Sprintf("%s %s · %s", dot[inc.Level], esc(inc.Text), ago(inc.FirstSeen, n.Time)))
		}
	}
	if len(others) > 0 {
		heading := "Also open"
		if n.Event == eventTest {
			heading = "Open right now"
		}
		fmt.Fprintf(&b, "\n\n<blockquote expandable><b>%s</b>\n%s</blockquote>", heading, strings.Join(others, "\n"))
	}

	var buttons []telegramButton
	if base := strings.TrimRight(n.PublicURL, "/"); base != "" {
		if len(n.Incidents) == 1 {
			if link := refLink(base, n.Incidents[0].Ref); link != base+"/#/" {
				buttons = append(buttons, telegramButton{"🔎 Details", link})
			}
		}
		if len(buttons) == 0 {
			buttons = append(buttons, telegramButton{"📊 Dashboard", base + "/#/"})
		}
		if !allCleared && n.Event != eventTest {
			buttons = append(buttons, telegramButton{"🔕 Mute", base + "/#/alerts"})
		}
	}
	return b.String(), buttons
}

type emailContent struct {
	From    string
	To      []string
	Subject string
	Text    string
	HTML    string
	Host    string
	Time    time.Time
}

type emailSender interface {
	via() string
	send(context.Context, emailContent) error
}

type EmailNotifier struct {
	from    string
	to      []string
	level   string
	senders []emailSender

	mu       sync.Mutex
	fallback string
}

func NewEmailNotifier(from, to, level string, senders ...emailSender) (*EmailNotifier, error) {
	var recipients []string
	for r := range strings.SplitSeq(to, ",") {
		if r = strings.TrimSpace(r); r != "" {
			recipients = append(recipients, r)
		}
	}
	if from == "" || len(recipients) == 0 {
		return nil, errors.New("email alerts need EMAIL_FROM and EMAIL_TO")
	}
	if len(senders) == 0 {
		return nil, errors.New("email alerts need a provider: SES_REGION, an email API key, or SMTP_URL")
	}
	return &EmailNotifier{from: from, to: recipients, level: level, senders: senders}, nil
}

func (e *EmailNotifier) Name() string  { return "Email" }
func (e *EmailNotifier) Level() string { return e.level }

func (e *EmailNotifier) Via() string {
	names := make([]string, len(e.senders))
	for i, s := range e.senders {
		names[i] = s.via()
	}
	return strings.Join(names, ", then ")
}

func (e *EmailNotifier) Note() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fallback
}

func (e *EmailNotifier) Send(ctx context.Context, n Notification) error {
	m := emailContent{From: e.from, To: e.to, Subject: subject(n), Text: emailText(n), HTML: emailHTML(n), Host: n.Host, Time: n.Time}
	var failures []string
	for i, s := range e.senders {
		attempt, cancel := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			attempt, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(e.senders)-i))
		}
		err := s.send(attempt, m)
		cancel()
		if err == nil {
			e.mu.Lock()
			e.fallback = ""
			if len(failures) > 0 {
				e.fallback = "Last sent by " + s.via() + " because " + strings.Join(failures, "; ")
			}
			e.mu.Unlock()
			if len(failures) > 0 {
				slog.Warn("email fell back", "via", s.via(), "failures", strings.Join(failures, "; "))
			}
			return nil
		}
		failures = append(failures, fmt.Sprintf("%s failed: %v", s.via(), err))
		if ctx.Err() != nil {
			break
		}
	}
	return errors.New("email: " + strings.Join(failures, "; "))
}

func emailSendersFromEnv() (senders []emailSender, problems []string) {
	if region := cmp.Or(os.Getenv("SES_REGION"), os.Getenv("AWS_REGION")); region != "" {
		ses, err := newSESSender(region, os.Getenv("SES_CONFIGURATION_SET"))
		if err != nil {
			problems = append(problems, "Amazon SES is off: "+err.Error())
		} else {
			senders = append(senders, ses)
		}
	}
	if key := os.Getenv("RESEND_API_KEY"); key != "" {
		senders = append(senders, resendSender(key))
	}
	if token := os.Getenv("POSTMARK_SERVER_TOKEN"); token != "" {
		senders = append(senders, postmarkSender(token))
	}
	if key := os.Getenv("MAILGUN_API_KEY"); key != "" {
		domain := os.Getenv("MAILGUN_DOMAIN")
		switch region := strings.ToLower(os.Getenv("MAILGUN_REGION")); {
		case domain == "":
			problems = append(problems, "Mailgun is off: set MAILGUN_DOMAIN to your sending domain")
		case region != "" && region != "us" && region != "eu":
			problems = append(problems, fmt.Sprintf("Mailgun is off: MAILGUN_REGION=%q must be us or eu", region))
		default:
			senders = append(senders, mailgunSender(key, domain, region))
		}
	}
	if key := os.Getenv("SENDGRID_API_KEY"); key != "" {
		senders = append(senders, sendgridSender(key))
	}
	if raw := os.Getenv("SMTP_URL"); raw != "" {
		smtpServer, err := newSMTPSender(raw)
		if err != nil {
			problems = append(problems, "SMTP is off: "+err.Error())
		} else {
			senders = append(senders, smtpServer)
		}
	}
	return senders, problems
}

type sesSender struct {
	client    *sesv2.Client
	creds     *aws.CredentialsCache
	region    string
	source    string
	configSet string
}

func newSESSender(region, configSet string) (*sesSender, error) {
	creds, source, err := awsCredentials()
	if err != nil {
		return nil, err
	}
	cache := aws.NewCredentialsCache(creds)
	opts := sesv2.Options{
		Region:      region,
		Credentials: cache,
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
	if endpoint := cmp.Or(os.Getenv("AWS_ENDPOINT_URL_SESV2"), os.Getenv("AWS_ENDPOINT_URL")); endpoint != "" {
		opts.BaseEndpoint = aws.String(endpoint)
	}
	return &sesSender{client: sesv2.New(opts), creds: cache, region: region, source: source, configSet: configSet}, nil
}

// Deliberately not config.LoadDefaultConfig: that links SSO, STS and sign-in clients a server never uses.
func awsCredentials() (aws.CredentialsProvider, string, error) {
	if id := os.Getenv("AWS_ACCESS_KEY_ID"); id != "" {
		secret := os.Getenv("AWS_SECRET_ACCESS_KEY")
		if secret == "" {
			return nil, "", errors.New("AWS_ACCESS_KEY_ID is set without AWS_SECRET_ACCESS_KEY")
		}
		return credentials.NewStaticCredentialsProvider(id, secret, os.Getenv("AWS_SESSION_TOKEN")), "access key", nil
	}
	endpoint := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if rel := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); rel != "" {
		endpoint = "http://169.254.170.2" + rel
	}
	if endpoint != "" {
		return endpointcreds.New(endpoint, func(o *endpointcreds.Options) {
			o.AuthorizationToken = os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
			if file := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); file != "" {
				o.AuthorizationTokenProvider = tokenFile(file)
			}
		}), "container role", nil
	}
	return ec2rolecreds.New(func(o *ec2rolecreds.Options) {
		o.Client = imds.New(imds.Options{HTTPClient: &http.Client{Timeout: 2 * time.Second}, Retryer: aws.NopRetryer{}})
	}), "instance role", nil
}

type tokenFile string

func (f tokenFile) GetToken() (string, error) {
	b, err := os.ReadFile(string(f))
	return strings.TrimSpace(string(b)), err
}

func (s *sesSender) via() string { return fmt.Sprintf("Amazon SES (%s, %s)", s.region, s.source) }

func (s *sesSender) send(ctx context.Context, m emailContent) error {
	if _, err := s.creds.Retrieve(ctx); err != nil {
		if s.source == "instance role" {
			return errors.New("no AWS credentials: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or run on EC2 with an instance role and a metadata hop limit of 2")
		}
		return fmt.Errorf("no AWS credentials from the %s: %v", s.source, err)
	}
	utf8 := func(text string) *sestypes.Content {
		return &sestypes.Content{Data: aws.String(text), Charset: aws.String("UTF-8")}
	}
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(m.From),
		Destination:      &sestypes.Destination{ToAddresses: m.To},
		Content: &sestypes.EmailContent{Simple: &sestypes.Message{
			Subject: utf8(m.Subject),
			Body:    &sestypes.Body{Text: utf8(m.Text), Html: utf8(m.HTML)},
		}},
	}
	if s.configSet != "" {
		in.ConfigurationSetName = aws.String(s.configSet)
	}
	_, err := s.client.SendEmail(ctx, in)
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return fmt.Errorf("%s: %s", apiErr.ErrorCode(), apiErr.ErrorMessage())
	}
	if retryErr, ok := errors.AsType[*retry.MaxAttemptsError](err); ok {
		err = retryErr.Err
	}
	return err
}

type apiSender struct {
	name    string
	api     string
	request func(api string, m emailContent) (*http.Request, error)
}

func (s *apiSender) via() string { return s.name }

func (s *apiSender) send(ctx context.Context, m emailContent) error {
	req, err := s.request(s.api, m)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return nil
	}
	var body struct {
		Message string `json:"message,case:ignore"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	json.UnmarshalRead(io.LimitReader(resp.Body, 64<<10), &body, lenient)
	if body.Message == "" && len(body.Errors) > 0 {
		body.Message = body.Errors[0].Message
	}
	return errors.New(cmp.Or(strings.TrimSpace(resp.Status+" "+body.Message), resp.Status))
}

func jsonRequest(url string, payload any, header http.Header) (*http.Request, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func resendSender(key string) *apiSender {
	return &apiSender{name: "Resend", api: "https://api.resend.com", request: func(api string, m emailContent) (*http.Request, error) {
		return jsonRequest(api+"/emails", map[string]any{"from": m.From, "to": m.To, "subject": m.Subject, "text": m.Text, "html": m.HTML},
			http.Header{"Authorization": {"Bearer " + key}})
	}}
}

func postmarkSender(token string) *apiSender {
	return &apiSender{name: "Postmark", api: "https://api.postmarkapp.com", request: func(api string, m emailContent) (*http.Request, error) {
		return jsonRequest(api+"/email", map[string]any{"From": m.From, "To": strings.Join(m.To, ","), "Subject": m.Subject, "TextBody": m.Text, "HtmlBody": m.HTML, "MessageStream": "outbound"},
			http.Header{"X-Postmark-Server-Token": {token}})
	}}
}

func mailgunSender(key, domain, region string) *apiSender {
	api := "https://api.mailgun.net"
	if region == "eu" {
		api = "https://api.eu.mailgun.net"
	}
	return &apiSender{name: "Mailgun", api: api, request: func(api string, m emailContent) (*http.Request, error) {
		form := url.Values{"from": {m.From}, "to": m.To, "subject": {m.Subject}, "text": {m.Text}, "html": {m.HTML}}
		req, err := http.NewRequest(http.MethodPost, api+"/v3/"+url.PathEscape(domain)+"/messages", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth("api", key)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}}
}

func sendgridSender(key string) *apiSender {
	type address struct {
		Email string `json:"email"`
		Name  string `json:"name,omitempty"`
	}
	parse := func(raw string) (address, error) {
		a, err := mail.ParseAddress(raw)
		if err != nil {
			return address{}, fmt.Errorf("%q is not an email address", raw)
		}
		return address{Email: a.Address, Name: a.Name}, nil
	}
	return &apiSender{name: "SendGrid", api: "https://api.sendgrid.com", request: func(api string, m emailContent) (*http.Request, error) {
		from, err := parse(m.From)
		if err != nil {
			return nil, err
		}
		to := make([]address, len(m.To))
		for i, r := range m.To {
			if to[i], err = parse(r); err != nil {
				return nil, err
			}
		}
		return jsonRequest(api+"/v3/mail/send", map[string]any{
			"personalizations": []map[string]any{{"to": to}},
			"from":             from,
			"subject":          m.Subject,
			"content":          []map[string]string{{"type": "text/plain", "value": m.Text}, {"type": "text/html", "value": m.HTML}},
		}, http.Header{"Authorization": {"Bearer " + key}})
	}}
}

type smtpSender struct {
	server   *url.URL
	insecure bool
}

func newSMTPSender(rawURL string) (*smtpSender, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "smtp" && u.Scheme != "smtps") || u.Hostname() == "" {
		return nil, errors.New("SMTP_URL must look like smtp://user:password@host:587 or smtps://user:password@host:465")
	}
	return &smtpSender{server: u, insecure: u.Query().Get("tls") == "none"}, nil
}

func (s *smtpSender) via() string { return "SMTP (" + s.server.Host + ")" }

func (s *smtpSender) send(ctx context.Context, m emailContent) error {
	host := s.server.Hostname()
	port := s.server.Port()
	if port == "" {
		port = map[string]string{"smtp": "587", "smtps": "465"}[s.server.Scheme]
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if s.server.Scheme == "smtps" {
		conn = tls.Client(conn, &tls.Config{ServerName: host})
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.server.Scheme == "smtp" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return err
			}
		} else if !s.insecure {
			return errors.New("the server does not offer STARTTLS; use smtps:// or add ?tls=none for a trusted local relay")
		}
	}
	if u := s.server.User; u != nil {
		password, _ := u.Password()
		if err := c.Auth(smtp.PlainAuth("", u.Username(), password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(bareAddress(m.From)); err != nil {
		return err
	}
	for _, r := range m.To {
		if err := c.Rcpt(bareAddress(r)); err != nil {
			return fmt.Errorf("%s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(emailMessage(m)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func bareAddress(raw string) string {
	if a, err := mail.ParseAddress(raw); err == nil {
		return a.Address
	}
	return raw
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

func emailText(n Notification) string {
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
	return body.String()
}

func emailMessage(m emailContent) []byte {
	var b bytes.Buffer
	parts := multipart.NewWriter(&b)
	fmt.Fprintf(&b, "From: %s\r\n", m.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(m.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", m.Time.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", uuid.NewV7(), messageIDHost(m.Host))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", parts.Boundary())
	for _, part := range []struct{ kind, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		w, _ := parts.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.kind + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		qp := quotedprintable.NewWriter(w)
		qp.Write([]byte(strings.ReplaceAll(part.body, "\n", "\r\n")))
		qp.Close()
	}
	parts.Close()
	return b.Bytes()
}

type emailTone struct {
	Tone, Bar, PillBg, PillInk string
}

var emailTones = map[string]emailTone{
	levelCrit: {"crit", "#d03b3b", "#fbe7e7", "#b02a2a"},
	levelWarn: {"warn", "#fab219", "#fdf1d6", "#8a5a00"},
	levelOK:   {"ok", "#0ca30c", "#e2f4e2", "#0a7a0a"},
	"":        {"info", "#6b7480", "#edf0f2", "#3d454e"},
}

var emailToneInk = map[string]string{"crit": "#b02a2a", "warn": "#8a5a00", "ok": "#0a7a0a"}

type emailRow struct {
	emailTone
	Label, Text, Meta, URL string
}

type emailFact struct {
	Label, Value, Tone string
}

type emailView struct {
	emailTone
	Subject, Preheader, Host, Label, Title, Summary  string
	Facts                                            []emailFact
	Rows, Others                                     []emailRow
	Action, ActionURL, DashboardURL, AlertsURL, Sent string
}

func emailHTML(n Notification) string {
	at := func(ms int64) string { return time.UnixMilli(ms).In(n.Time.Location()).Format("15:04") }
	when := func(ms int64) string {
		t := time.UnixMilli(ms).In(n.Time.Location())
		if y, m, d := t.Date(); y == n.Time.Year() && m == n.Time.Month() && d == n.Time.Day() {
			return t.Format("15:04 MST")
		}
		return t.Format("2 Jan, 15:04 MST")
	}
	row := func(inc Incident, event string) emailRow {
		r := emailRow{Text: inc.Text, URL: refLink(n.PublicURL, inc.Ref)}
		if event == eventResolved {
			r.emailTone, r.Label = emailTones[levelOK], "Resolved"
			r.Meta = fmt.Sprintf("lasted %s · cleared at %s", humanDuration(float64(inc.ResolvedAt-inc.FirstSeen)/1000), at(inc.ResolvedAt))
		} else {
			r.emailTone, r.Label = emailTones[inc.Level], levelLabel(inc.Level)
			r.Meta = fmt.Sprintf("for %s · since %s", ago(inc.FirstSeen, n.Time), at(inc.FirstSeen))
		}
		return r
	}
	base := strings.TrimRight(n.PublicURL, "/")
	v := emailView{Subject: subject(n), Host: n.Host, Sent: n.Time.Format("2 Jan 2006 at 15:04 MST")}
	if base != "" {
		v.Action, v.ActionURL, v.DashboardURL, v.AlertsURL = "Open the dashboard", base+"/#/", base+"/#/", base+"/#/alerts"
	}
	worst := worstLevel(n.Incidents)
	switch n.Event {
	case eventTest:
		v.emailTone, v.Label = emailTones[""], "Test"
		v.Title = "Alerts reach this inbox"
		v.Summary = "This is a test. Nothing is wrong, and there is nothing to do."
		open := "None"
		if len(n.Open) > 0 {
			open = fmt.Sprintf("%d, listed below", len(n.Open))
		}
		v.Facts = []emailFact{{"Server", n.Host, ""}, {"Problems open now", open, ""}}
	case eventResolved:
		v.emailTone, v.Label = emailTones[levelOK], "Resolved"
	default:
		v.emailTone, v.Label = emailTones[worst], levelLabel(worst)
		if n.Event == eventEscalated {
			v.Label = "Now critical"
		} else if n.Event == eventReminder {
			v.Label = "Still failing"
		}
	}
	if n.Event != eventTest {
		for _, inc := range n.Incidents {
			v.Rows = append(v.Rows, row(inc, n.Event))
		}
		if len(v.Rows) == 1 {
			only, inc := v.Rows[0], n.Incidents[0]
			v.Title, v.Rows = only.Text, nil
			if n.Event == eventResolved {
				lasted := humanDuration(float64(inc.ResolvedAt-inc.FirstSeen) / 1000)
				v.Summary = "This has cleared. No action is needed."
				v.Preheader = fmt.Sprintf("Cleared at %s on %s after %s.", at(inc.ResolvedAt), n.Host, lasted)
				v.Facts = []emailFact{{"Status", "Resolved", "ok"}, {"Server", n.Host, ""}, {"Started", when(inc.FirstSeen), ""}, {"Cleared", when(inc.ResolvedAt), ""}, {"Lasted", lasted, ""}}
			} else {
				v.Summary = map[string]string{eventFiring: "This needs attention.", eventEscalated: "This was a warning and has become critical.", eventReminder: "This is still failing. Here's a reminder."}[n.Event]
				v.Preheader = fmt.Sprintf("%s for %s, since %s, on %s.", levelLabel(inc.Level), ago(inc.FirstSeen, n.Time), at(inc.FirstSeen), n.Host)
				v.Facts = []emailFact{{"Severity", levelLabel(inc.Level), emailTones[inc.Level].Tone}, {"Server", n.Host, ""}, {"Started", when(inc.FirstSeen), ""}, {"Failing for", ago(inc.FirstSeen, n.Time), ""}}
			}
			if only.URL != "" {
				v.Action, v.ActionURL = "See what's happening", only.URL
				if n.Event == eventResolved {
					v.Action = "Open its page"
				}
			}
		} else if n.Event == eventResolved {
			v.Title, v.Summary = fmt.Sprintf("%d problems resolved", len(v.Rows)), "Everything below has cleared on "+n.Host+"."
		} else {
			v.Title = fmt.Sprintf("%d problems on %s", len(v.Rows), n.Host)
			v.Summary = map[string]string{eventFiring: "They started within moments of each other.", eventEscalated: "These are now critical.", eventReminder: "These are still failing."}[n.Event]
		}
	}
	shown := map[string]bool{}
	for _, inc := range n.Incidents {
		shown[inc.Key] = true
	}
	for _, inc := range n.Open {
		if !shown[inc.Key] {
			v.Others = append(v.Others, row(inc, ""))
		}
	}
	v.Preheader = cmp.Or(v.Preheader, v.Summary)
	var b strings.Builder
	if err := alertEmail.Execute(&b, v); err != nil {
		slog.Warn("email template failed", "err", err)
	}
	return b.String()
}

func levelLabel(level string) string {
	if level == levelCrit {
		return "Critical"
	}
	return "Warning"
}

// html/template drops comments, so Outlook's conditional ones have to be injected as trusted HTML.
var alertEmail = template.Must(template.New("email.html").Funcs(template.FuncMap{
	"outlookOpen": func() template.HTML {
		return `<!--[if mso]><table role="presentation" width="600" align="center" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`
	},
	"outlookClose": func() template.HTML { return `<!--[if mso]></td></tr></table><![endif]-->` },
	"outlookHead": func() template.HTML {
		return `<!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript><style>table, td, div, h1, p, a, span { font-family: Arial, Helvetica, sans-serif !important; }</style><![endif]-->`
	},
	"factColor":   func(tone string) string { return cmp.Or(emailToneInk[tone], "#0f1317") },
	"previewStop": func() template.HTML { return template.HTML(strings.Repeat("&#8199;&#847;", 90)) },
}).ParseFS(templates.Files, "email.html"))

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
	token, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	switch {
	case token != "" && chat != "":
		tg := NewTelegramNotifier(token, chat, level("TELEGRAM_LEVEL", levelWarn))
		if api := os.Getenv("TELEGRAM_API_URL"); api != "" {
			tg.api = strings.TrimRight(api, "/")
		}
		if raw := os.Getenv("TELEGRAM_TOPIC_ID"); raw != "" {
			if topic, err := strconv.Atoi(raw); err == nil && topic > 0 {
				tg.topic = topic
			} else {
				cfg.Problems = append(cfg.Problems, fmt.Sprintf("TELEGRAM_TOPIC_ID=%q must be a topic's number", raw))
			}
		}
		if raw := os.Getenv("TELEGRAM_QUIET_HOURS"); raw != "" {
			if from, to, err := parseQuietHours(raw); err == nil {
				tg.quietFrom, tg.quietTo = from, to
			} else {
				cfg.Problems = append(cfg.Problems, err.Error())
			}
		}
		if cfg.StateFile != "" {
			tg.stateFile = filepath.Join(filepath.Dir(cfg.StateFile), "telegram.json")
			tg.load()
		}
		cfg.Channels = append(cfg.Channels, tg)
	case token != "" || chat != "":
		cfg.Problems = append(cfg.Problems, "Telegram alerts need both TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID")
	}
	senders, problems := emailSendersFromEnv()
	cfg.Problems = append(cfg.Problems, problems...)
	if len(senders) > 0 || os.Getenv("EMAIL_TO") != "" {
		email, err := NewEmailNotifier(os.Getenv("EMAIL_FROM"), os.Getenv("EMAIL_TO"), level("EMAIL_LEVEL", levelCrit), senders...)
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
