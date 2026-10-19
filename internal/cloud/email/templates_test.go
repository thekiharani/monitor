package email

import (
	"strings"
	"testing"
)

func sample(name string) any {
	switch name {
	case "login_code":
		return LoginCodeData{Code: "123456", ExpiresIn: "10 minutes"}
	case "team_invite":
		return map[string]any{"role": "admin", "accept_url": "https://monitor.noria.co.ke/app/invite/abc123", "expires_at": "12 September 2026"}
	case "email_change_code":
		return EmailChangeCodeData{Code: "123456", ExpiresIn: "10 minutes", Current: "old@example.com"}
	case "email_changed":
		return EmailChangedData{Previous: "old@example.com", Current: "new@example.com"}
	case "billing_renewal":
		return map[string]any{"plan_name": "Pro", "pay_link": "https://checkout.paystack.com/abc123"}
	case "monitor_alert":
		return map[string]any{
			"event": "down", "tone": "crit", "label": "Down", "headline": "Website is down", "detail": "It is returning HTTP 503.",
			"monitor_name": "Website", "target": "https://example.com", "cause": "returning HTTP 503", "regions": "fra, iad",
			"started_at": "09:14 UTC, 16 October 2026", "dashboard_url": "https://monitor.noria.co.ke/app/monitors/abc", "workspace": "Acme",
		}
	case "probe_status":
		return map[string]any{
			"tone": "crit", "label": "Probe offline", "headline": "The eu-west-1 probe stopped reporting",
			"detail":     "No other probe is online in eu-west, so checks from that region have stopped.",
			"probe_name": "eu-west-1", "region": "eu-west", "last_seen": "09:14 UTC, 16 October 2026",
			"region_state": "No probe online", "control_url": "https://monitor.noria.co.ke/control/probes",
		}
	default:
		return map[string]any{}
	}
}

func mustRender(t *testing.T, name string) Message {
	t.Helper()
	message, err := Render(name, sample(name))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestEveryTemplateRendersBothAlternativesInTheSharedFrame(t *testing.T) {
	for name := range subjects {
		t.Run(name, func(t *testing.T) {
			message := mustRender(t, name)
			for label, body := range map[string]string{"subject": message.Subject, "html": message.HTML, "text": message.Text} {
				if strings.TrimSpace(body) == "" {
					t.Fatalf("%s alternative is empty", label)
				}
				if strings.Contains(body, "{{") || strings.Contains(body, "<no value>") || strings.Contains(body, "<nil>") {
					t.Errorf("%s alternative has an unresolved value", label)
				}
			}
			for _, required := range []string{"Noria Monitor", "You are receiving this because", "max-width:600px", `name="color-scheme" content="light dark"`, "prefers-color-scheme: dark"} {
				if !strings.Contains(message.HTML, required) {
					t.Errorf("html alternative missing %q", required)
				}
			}
		})
	}
}

func TestLoginCodeShowsTheCodeAndNoButton(t *testing.T) {
	message := mustRender(t, "login_code")
	if message.Subject != "Your Noria Monitor login code" {
		t.Errorf("subject = %q", message.Subject)
	}
	for _, body := range []string{message.HTML, message.Text} {
		if !strings.Contains(body, "123456") || !strings.Contains(body, "10 minutes") {
			t.Error("code or expiry missing")
		}
	}
	if strings.Contains(message.HTML, `class="btn"`) {
		t.Error("a login code must not carry a call to action")
	}
}

func TestEveryCallToActionIsCentredLinkedAndHasAFallback(t *testing.T) {
	for name, label := range map[string]string{
		"team_invite": "Accept invitation", "billing_renewal": "Pay for Pro", "payment_failed": "Settle the payment",
		"subscription_canceled": "Choose a plan", "subscription_expiring_card": "Update payment method", "monitor_alert": "Open monitor",
	} {
		t.Run(name, func(t *testing.T) {
			html := mustRender(t, name).HTML
			end := strings.Index(html, ">"+label+"</a>")
			if end < 0 {
				t.Fatalf("missing the %q button", label)
			}
			cell := html[strings.LastIndex(html[:end], `<td class="btn"`):end]
			if !strings.Contains(cell, `align="center"`) {
				t.Error("button cell is not centred")
			}
			href := cell[strings.Index(cell, `href="`)+6:]
			href = href[:strings.IndexByte(href, '"')]
			if href == "" || strings.HasPrefix(href, "#") {
				t.Fatal("button has no destination")
			}
			if !strings.Contains(html, "Copy and paste this link") || strings.Count(html, href) < 2 {
				t.Error("button has no plain-link fallback")
			}
		})
	}
}

func TestAlertToneDrivesTheAccentAndFactsSkipEmptyValues(t *testing.T) {
	down := mustRender(t, "monitor_alert")
	if down.Subject != "Website is down" || !strings.Contains(down.HTML, "border-top:4px solid #d92d20") {
		t.Errorf("a down alert is not red: subject=%q", down.Subject)
	}
	for _, fact := range []string{"returning HTTP 503", "fra, iad", "09:14 UTC, 16 October 2026", "Acme"} {
		if !strings.Contains(down.HTML, fact) || !strings.Contains(down.Text, fact) {
			t.Errorf("fact %q missing", fact)
		}
	}
	if strings.Contains(down.HTML, "Recovered") || strings.Contains(down.HTML, "Certificate expires") {
		t.Error("facts without a value were rendered")
	}

	up, err := Render("monitor_alert", map[string]any{"event": "up", "tone": "ok", "label": "Recovered", "headline": "Website is back up after 4m 2s", "detail": "The incident is resolved.", "downtime": "4m 2s"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(up.HTML, "border-top:4px solid #12b76a") || !strings.Contains(up.HTML, "4m 2s") {
		t.Error("a recovery alert is not green or lost its downtime")
	}
}

func TestConfigureDrivesLinksFromTheAppOrigin(t *testing.T) {
	t.Cleanup(func() { brand.Store(nil) })
	Configure("https://example.test/")
	html := mustRender(t, "payment_failed").HTML
	if !strings.Contains(html, "https://example.test/app/billing") {
		t.Error("html alternative does not link to the configured origin")
	}
	if strings.Contains(html, "https://example.test//") {
		t.Error("a trailing slash on the origin produced a doubled separator")
	}
}

func TestRenderRejectsUnknownTemplate(t *testing.T) {
	if _, err := Render("does_not_exist", nil); err == nil {
		t.Error("Render(does_not_exist) = nil error, want an error")
	}
}
