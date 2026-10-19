package usage

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thekiharani/monitor/internal/cloud/config"
)

type Limits struct {
	MaxMonitors        int     `json:"monitors"`
	MinIntervalSeconds int     `json:"min_interval_seconds"`
	MaxRegions         int     `json:"regions"`
	MaxStatusPages     int     `json:"status_pages"`
	MaxCustomDomains   int     `json:"custom_domains"`
	MaxAlertChannels   int     `json:"alert_channels"`
	RetentionDays      int     `json:"retention_days"`
	MaxAPIKeys         int     `json:"api_keys"`
	MonthlyAPIRequests int64   `json:"monthly_api_requests"`
	RequestsPerSecond  float64 `json:"requests_per_second"`
	Burst              int     `json:"burst"`
	MaxMembers         int     `json:"team_members"`
}

var plans = map[string]Limits{
	"free":     {MaxMonitors: 10, MinIntervalSeconds: 300, MaxRegions: 1, MaxStatusPages: 1, MaxCustomDomains: 0, MaxAlertChannels: 2, RetentionDays: 30, MaxAPIKeys: 1, MonthlyAPIRequests: 10000, RequestsPerSecond: 2, Burst: 5, MaxMembers: 1},
	"starter":  {MaxMonitors: 25, MinIntervalSeconds: 120, MaxRegions: 2, MaxStatusPages: 2, MaxCustomDomains: 1, MaxAlertChannels: 5, RetentionDays: 60, MaxAPIKeys: 3, MonthlyAPIRequests: 100000, RequestsPerSecond: 10, Burst: 20, MaxMembers: 3},
	"pro":      {MaxMonitors: 50, MinIntervalSeconds: 60, MaxRegions: 3, MaxStatusPages: 5, MaxCustomDomains: 5, MaxAlertChannels: 20, RetentionDays: 90, MaxAPIKeys: 10, MonthlyAPIRequests: 500000, RequestsPerSecond: 50, Burst: 100, MaxMembers: 10},
	"business": {MaxMonitors: 200, MinIntervalSeconds: 30, MaxRegions: 10, MaxStatusPages: 20, MaxCustomDomains: 20, MaxAlertChannels: 50, RetentionDays: 365, MaxAPIKeys: 25, MonthlyAPIRequests: 2000000, RequestsPerSecond: 100, Burst: 200, MaxMembers: 25},
	"control":  {RequestsPerSecond: 20, Burst: 60},
}

var CustomerPlans = []string{"free", "starter", "pro", "business"}

func LongestRetention() int {
	longest := 0
	for _, plan := range CustomerPlans {
		longest = max(longest, LimitsFor(plan).RetentionDays)
	}
	return longest
}

func LimitsFor(plan string) Limits {
	if l, ok := plans[plan]; ok {
		return l
	}
	return plans["free"]
}

type Plan struct {
	Plan        string `json:"plan"`
	Currency    string `json:"currency"`
	PriceMinor  int64  `json:"price_minor"`
	KESPlanCode string `json:"kes_plan_code"`
	USDPlanCode string `json:"usd_plan_code"`
	Version     int64  `json:"version"`
	Limits
}

type Catalog struct {
	cfg     config.Config
	mu      sync.Mutex
	current atomic.Pointer[map[string]Plan]
}

func NewCatalog(cfg config.Config) *Catalog {
	c := &Catalog{cfg: cfg}
	defaults := c.defaults()
	c.current.Store(&defaults)
	return c
}

func (c *Catalog) defaults() map[string]Plan {
	out := map[string]Plan{}
	for _, name := range CustomerPlans {
		kes, _ := c.cfg.Pricing(name, "KES")
		usd, _ := c.cfg.Pricing(name, "USD")
		out[name] = Plan{Plan: name, Currency: "KES", PriceMinor: kes.Amount, KESPlanCode: kes.Code, USDPlanCode: usd.Code, Limits: LimitsFor(name)}
	}
	return out
}

func (c *Catalog) Get(name string) (Plan, bool) {
	p, ok := (*c.current.Load())[name]
	return p, ok
}

func (c *Catalog) List() []Plan {
	out := make([]Plan, 0, len(CustomerPlans))
	for _, name := range CustomerPlans {
		p, _ := c.Get(name)
		out = append(out, p)
	}
	return out
}

func (c *Catalog) LimitsFor(name string) Limits {
	if p, ok := c.Get(name); ok {
		return p.Limits
	}
	if name == "control" {
		return LimitsFor(name)
	}
	p, _ := c.Get("free")
	return p.Limits
}

func (c *Catalog) Pricing(name, currency string) (config.PlanPricing, bool) {
	p, ok := c.Get(name)
	if !ok || name == "free" {
		return config.PlanPricing{}, false
	}
	pricing := config.PlanPricing{Code: p.KESPlanCode, Amount: p.PriceMinor, Currency: currency}
	if currency == "USD" {
		pricing.Code = p.USDPlanCode
		pricing.Amount = p.PriceMinor/100 + (p.PriceMinor%100+50)/100
	} else if currency != "KES" {
		return config.PlanPricing{}, false
	}
	return pricing, true
}

func (c *Catalog) Reload(ctx context.Context, db *pgxpool.Pool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := db.Query(ctx, `SELECT plan,definition,version FROM plan_catalog`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := c.defaults()
	for rows.Next() {
		var name string
		var definition []byte
		var version int64
		if err = rows.Scan(&name, &definition, &version); err != nil {
			return err
		}
		var p Plan
		if err = json.Unmarshal(definition, &p); err != nil {
			return err
		}
		p.Plan = name
		p.Version = version
		if err = p.Validate(); err != nil {
			return err
		}
		next[name] = p
	}
	if err = rows.Err(); err != nil {
		return err
	}
	c.current.Store(&next)
	return nil
}

func (p Plan) Validate() error {
	validName := false
	for _, name := range CustomerPlans {
		if p.Plan == name {
			validName = true
		}
	}
	if !validName || p.Currency != "KES" || p.Version < 0 {
		return fmt.Errorf("invalid plan identity or base currency")
	}
	if p.PriceMinor < 0 || p.PriceMinor > 100000000000 || (p.Plan == "free" && p.PriceMinor != 0) || (p.Plan != "free" && p.PriceMinor < 100) {
		return fmt.Errorf("invalid base price")
	}
	if p.MaxMonitors < 1 || p.MaxMonitors > 1000000 || p.MinIntervalSeconds < 30 || p.MinIntervalSeconds > 86400 || p.MaxRegions < 1 || p.MaxRegions > 16 {
		return fmt.Errorf("invalid monitor, interval or region limits")
	}
	if p.MaxStatusPages < 1 || p.MaxStatusPages > 10000 || p.MaxCustomDomains < 0 || p.MaxCustomDomains > p.MaxStatusPages || p.MaxAlertChannels < 1 || p.MaxAlertChannels > 10000 {
		return fmt.Errorf("invalid status page, domain or alert limits")
	}
	if p.RetentionDays < 1 || p.RetentionDays > 3650 || p.MaxAPIKeys < 1 || p.MaxAPIKeys > 10000 || p.MaxMembers < 1 || p.MaxMembers > 10000 {
		return fmt.Errorf("invalid retention, key or team limits")
	}
	if p.MonthlyAPIRequests < 1 || p.MonthlyAPIRequests > 1000000000000 || p.RequestsPerSecond <= 0 || p.RequestsPerSecond > 100000 || p.Burst < 1 || p.Burst > 1000000 {
		return fmt.Errorf("invalid API request limits")
	}
	if len(p.KESPlanCode) > 128 || len(p.USDPlanCode) > 128 || (p.KESPlanCode != "" && p.KESPlanCode == p.USDPlanCode) {
		return fmt.Errorf("invalid currency plan codes")
	}
	return nil
}
