package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/email"
)

func main() {
	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if c.Env == "production" {
		log.Fatal("email preview is disabled in production")
	}
	to := "preview@example.test"
	if len(os.Args) > 1 && os.Args[1] != "" {
		to = os.Args[1]
	}
	sender, err := email.New(context.Background(), c)
	if err != nil {
		log.Fatal(err)
	}
	messages := []email.Message{}
	for _, item := range []struct {
		name string
		data any
	}{
		{name: "login_code", data: email.LoginCodeData{Code: "482731", ExpiresIn: email.HumanDuration(c.OTPTTL)}},
		{name: "team_invite", data: map[string]any{
			"role":       "admin",
			"accept_url": c.AppOrigin + "/app/invite/preview-token",
			"expires_at": time.Now().AddDate(0, 0, 7).Format("2 January 2006"),
		}},
		{name: "billing_renewal", data: map[string]any{
			"plan_name": "Pro",
			"pay_link":  c.AppOrigin + "/app/billing",
		}},
		{name: "payment_failed"},
		{name: "subscription_canceled"},
		{name: "subscription_expiring_card"},
	} {
		message, err := email.Render(item.name, item.data)
		if err != nil {
			log.Fatal(err)
		}
		message.To = to
		messages = append(messages, message)
	}
	for _, message := range messages {
		if err := sender.Send(context.Background(), message); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("sent %d email previews to %s", len(messages), to)
}
