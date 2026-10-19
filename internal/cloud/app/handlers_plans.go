package app

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

type planView struct {
	Plan               string `json:"plan"`
	Currency           string `json:"currency"`
	PriceMinor         int64  `json:"price_minor"`
	Monitors           int    `json:"monitors"`
	MinIntervalSeconds int    `json:"min_interval_seconds"`
	Regions            int    `json:"regions"`
	StatusPages        int    `json:"status_pages"`
	CustomDomains      int    `json:"custom_domains"`
	AlertChannels      int    `json:"alert_channels"`
	RetentionDays      int    `json:"retention_days"`
	APIKeys            int    `json:"api_keys"`
	MonthlyRequests    int64  `json:"monthly_api_requests"`
	Members            int    `json:"team_members"`
}

func (a *App) publicPlans(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin, a.cfg.DashboardOrigin) {
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("currency")))
	if currency == "" {
		currency = "KES"
	}
	if currency != "KES" && currency != "USD" {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	out := make([]planView, 0, len(usage.CustomerPlans))
	for _, plan := range usage.CustomerPlans {
		limits := a.planLimits(plan)
		pricing, _ := a.paidPlan(plan, currency)
		out = append(out, planView{
			Plan:               plan,
			Currency:           currency,
			PriceMinor:         pricing.Amount,
			Monitors:           limits.MaxMonitors,
			MinIntervalSeconds: limits.MinIntervalSeconds,
			Regions:            limits.MaxRegions,
			StatusPages:        limits.MaxStatusPages,
			CustomDomains:      limits.MaxCustomDomains,
			AlertChannels:      limits.MaxAlertChannels,
			RetentionDays:      limits.RetentionDays,
			APIKeys:            limits.MaxAPIKeys,
			MonthlyRequests:    limits.MonthlyAPIRequests,
			Members:            limits.MaxMembers,
		})
	}
	w.Header().Set("Cache-Control", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) usage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	var used int64
	var keysUsed, membersUsed int
	period := time.Now().UTC().Format("2006-01") + "-01"
	if err := a.tenantRow(r.Context(), p.AccountID.String(), `
		SELECT
			COALESCE((SELECT api_requests FROM account_monthly_usage WHERE account_id=$1 AND period=$2::date),0),
			(SELECT count(*) FROM credentials WHERE account_id=$1 AND audience='customer_api' AND revoked_at IS NULL AND expires_at>now()),
			(SELECT count(*) FROM account_memberships WHERE account_id=$1)`,
		[]any{p.AccountID.String(), period},
		&used, &keysUsed, &membersUsed); err != nil {
		a.logError("usage query failed", "usage_api", "read_account_usage", err, "account_id", p.AccountID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	lim := a.planLimits(p.Plan)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"plan":           p.Plan,
			"period":         time.Now().UTC().Format("2006-01"),
			"api_requests":   map[string]any{"used": used, "limit": lim.MonthlyAPIRequests},
			"api_keys":       map[string]any{"used": keysUsed, "limit": lim.MaxAPIKeys},
			"members":        map[string]any{"used": membersUsed, "limit": lim.MaxMembers},
			"retention_days": lim.RetentionDays,
		},
	})
}

func (a *App) live(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) planLimits(name string) usage.Limits {
	if a.plans != nil {
		return a.plans.LimitsFor(name)
	}
	return usage.LimitsFor(name)
}

func (a *App) InitializePlans(ctx context.Context) error {
	if a.plans == nil {
		a.plans = usage.NewCatalog(a.cfg)
	}
	return a.plans.Reload(ctx, a.db)
}

func (a *App) plansWorker(ctx context.Context) {
	if a.plans == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := a.plans.Reload(ctx, a.db); err != nil {
				a.logError("plan catalog refresh failed", "plans_worker", "reload_plans", err)
			}
		case <-ctx.Done():
			return
		case <-a.stop:
			return
		}
	}
}
