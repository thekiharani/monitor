package notify

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	path string
	body map[string]any
	raw  []byte
	head http.Header
}

func recorder(t *testing.T, reply func(path string) (int, string)) (*httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var calls []recorded
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, recorded{path: r.URL.Path, body: body, raw: raw, head: r.Header.Clone()})
		mu.Unlock()
		status, text := reply(r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
	}))
	t.Cleanup(server.Close)
	return server, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), calls...)
	}
}

func downEvent() Event {
	started := time.Now().Add(-4 * time.Minute)
	return Event{
		Kind: EventDown, MonitorName: "Website", Workspace: "Acme", Target: "https://example.com",
		Cause: "returning HTTP 503", RegionsDown: []string{"fra", "iad"}, StartedAt: &started,
		DashboardURL: "https://monitor.noria.co.ke/app/monitors/abc",
	}
}

func TestTelegramPinsADownAlertAndResolvesItInPlace(t *testing.T) {
	server, calls := recorder(t, func(string) (int, string) {
		return 200, `{"ok":true,"result":{"message_id":42}}`
	})
	tg := Telegram{Token: "T", ChatID: "-100123", TopicID: 7, APIBase: server.URL, Client: Client(5*time.Second, true)}

	delivery, err := tg.Send(t.Context(), downEvent())
	if err != nil || delivery.MessageID != "42" {
		t.Fatalf("down: %v %+v", err, delivery)
	}
	got := calls()
	if len(got) != 2 || got[0].path != "/botT/sendMessage" || got[1].path != "/botT/pinChatMessage" {
		t.Fatalf("down calls: %+v", got)
	}
	text := got[0].body["text"].(string)
	for _, want := range []string{"🔴 <b>DOWN</b>", "<code>Acme</code>", "<b>Website</b>", "returning HTTP 503", "Confirmed from fra, iad"} {
		if !strings.Contains(text, want) {
			t.Errorf("down text missing %q:\n%s", want, text)
		}
	}
	if got[0].body["disable_notification"] != false || got[0].body["message_thread_id"].(float64) != 7 {
		t.Errorf("a down alert must ring and land in the topic: %+v", got[0].body)
	}

	resolved := time.Now()
	up := downEvent()
	up.Kind, up.ResolvedAt, up.ReplyTo = EventUp, &resolved, "42"
	if _, err = tg.Send(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	got = calls()[2:]
	if len(got) != 3 || got[0].path != "/botT/editMessageText" || got[1].path != "/botT/unpinChatMessage" || got[2].path != "/botT/sendMessage" {
		t.Fatalf("up calls: %+v", got)
	}
	if edited := got[0].body["text"].(string); !strings.Contains(edited, "✅ <b>RESOLVED</b>") || !strings.Contains(edited, "down for 4m") {
		t.Errorf("the original post was not marked resolved:\n%s", edited)
	}
	reply := got[2].body
	if reply["disable_notification"] != true || reply["reply_parameters"].(map[string]any)["message_id"].(float64) != 42 {
		t.Errorf("recovery must be a quiet reply to the original: %+v", reply)
	}
}

func TestTelegramReportsTheBotAPIsReason(t *testing.T) {
	server, _ := recorder(t, func(string) (int, string) {
		return 403, `{"ok":false,"description":"Forbidden: bot was kicked from the group chat"}`
	})
	_, err := Telegram{Token: "T", ChatID: "1", APIBase: server.URL, Client: Client(time.Second, true)}.Send(t.Context(), downEvent())
	if err == nil || !strings.Contains(err.Error(), "bot was kicked") {
		t.Fatalf("err = %v", err)
	}
}

func TestSlackSendsAHeadlineAndAButton(t *testing.T) {
	server, calls := recorder(t, func(string) (int, string) { return 200, "ok" })
	if _, err := (Slack{WebhookURL: server.URL + "/services/x", Client: Client(time.Second, true)}).Send(t.Context(), downEvent()); err != nil {
		t.Fatal(err)
	}
	body := calls()[0].body
	if body["text"] != "Website is down" || !strings.Contains(string(calls()[0].raw), "Open monitor") {
		t.Fatalf("slack body: %s", calls()[0].raw)
	}
}

func TestWebhookIsSignedOverTheExactBody(t *testing.T) {
	server, calls := recorder(t, func(string) (int, string) { return 204, "" })
	at := time.Unix(1_800_000_000, 0)
	hook := Webhook{URL: server.URL, Secret: "whsec_test", Client: Client(time.Second, true), Now: func() time.Time { return at }}
	if _, err := hook.Send(t.Context(), downEvent()); err != nil {
		t.Fatal(err)
	}
	call := calls()[0]
	if call.head.Get("X-Noria-Signature") != Sign("whsec_test", at, call.raw) || call.head.Get("X-Noria-Event") != "monitor.down" {
		t.Fatalf("signature header %q does not match the body", call.head.Get("X-Noria-Signature"))
	}
	if call.body["type"] != "monitor.down" || call.body["data"].(map[string]any)["monitor_name"] != "Website" {
		t.Fatalf("webhook body: %s", call.raw)
	}
}

func TestFailedDeliveriesCarryTheEndpointsAnswer(t *testing.T) {
	server, _ := recorder(t, func(string) (int, string) { return 500, "upstream exploded" })
	_, err := Webhook{URL: server.URL, Secret: "s", Client: Client(time.Second, true)}.Send(t.Context(), downEvent())
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheAlertClientRefusesPrivateAddresses(t *testing.T) {
	server, calls := recorder(t, func(string) (int, string) { return 200, "" })
	_, err := Webhook{URL: server.URL, Secret: "s", Client: Client(time.Second, false)}.Send(t.Context(), downEvent())
	if err == nil || !strings.Contains(err.Error(), "private address") || len(calls()) != 0 {
		t.Fatalf("a webhook reached a loopback address: err=%v calls=%d", err, len(calls()))
	}
}

func TestHeadlinesReadNaturally(t *testing.T) {
	started := time.Now().Add(-90 * time.Minute)
	resolved := time.Now()
	expires := time.Now().Add(9*24*time.Hour + time.Hour)
	for _, tc := range []struct {
		event Event
		want  string
	}{
		{Event{Kind: EventDown, MonitorName: "API"}, "API is down"},
		{Event{Kind: EventUp, MonitorName: "API", StartedAt: &started, ResolvedAt: &resolved}, "API is back up after 1h 30m"},
		{Event{Kind: EventCertificate, MonitorName: "API", CertExpiresAt: &expires}, "API's certificate expires in 9 days"},
		{Event{Kind: EventTest}, "Test alert from Noria Monitor"},
	} {
		if got := tc.event.Headline(); got != tc.want {
			t.Errorf("Headline() = %q, want %q", got, tc.want)
		}
	}
}
