package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/notify"
	"github.com/thekiharani/monitor/internal/cloud/templates"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const (
	statusCacheTTL    = 30 * time.Second
	statusBarDays     = 90
	statusHistoryDays = 14
	maxStatusItems    = 100
)

var (
	statusPageTemplate = template.Must(template.ParseFS(templates.Files, "statuspage.html"))
	badgeTemplate      = template.Must(template.New("badge.svg").ParseFS(templates.Files, "badge.svg"))
	slugPattern        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{1,46}[a-z0-9])$`)
	hostnamePattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	postStatuses       = map[string][]string{
		"incident":    {"investigating", "identified", "monitoring", "resolved"},
		"maintenance": {"scheduled", "in_progress", "completed"},
	}
)

type statusCache struct {
	mu      sync.Mutex
	entries map[string]statusCacheEntry
}

type statusCacheEntry struct {
	page    *statusPage
	expires time.Time
}

func newStatusCache() *statusCache { return &statusCache{entries: map[string]statusCacheEntry{}} }

func (c *statusCache) get(key string) (*statusPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.page, true
}

func (c *statusCache) put(key string, page *statusPage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 10000 {
		c.entries = map[string]statusCacheEntry{}
	}
	c.entries[key] = statusCacheEntry{page: page, expires: time.Now().Add(statusCacheTTL)}
}

func (c *statusCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]statusCacheEntry{}
}

type statusPage struct {
	ID, AccountID, Slug, Title, Description, HomepageURL, LogoURL, Theme string
	CustomDomain                                                         string
	DomainVerified, ShowResponseTimes                                    bool
	Overall                                                              statusOverall
	Groups                                                               []statusGroup
	Active                                                               []statusPost
	History                                                              []statusDay
	GeneratedAt                                                          time.Time
}

type statusOverall struct {
	Tone, Text, Indicator string
}

type statusGroup struct {
	Name  string
	Items []statusItem
}

type statusItem struct {
	ID, Name, Group, State, StateText, Uptime, ResponseMs string
	UptimeValue                                           float64
	Days                                                  []statusBar
}

type statusBar struct {
	Tone, Title string
}

type statusPost struct {
	ID, Kind, Impact, Title, Status, StatusText, When, Summary string
	StartsAt                                                   time.Time
	ResolvedAt                                                 *time.Time
	Updates                                                    []statusUpdate
}

type statusUpdate struct {
	Status, When, Body string
}

type statusDay struct {
	Date  string
	Posts []statusPost
}

func humanStatus(s string) string { return strings.ReplaceAll(s, "_", " ") }

func statusTime(t time.Time) string { return t.UTC().Format("2 Jan 2006, 15:04 UTC") }

func (a *App) loadStatusPage(ctx context.Context, slug, domain string) (*statusPage, error) {
	key := "slug:" + slug
	if domain != "" {
		key = "domain:" + domain
	}
	if page, ok := a.statusPages.get(key); ok {
		return page, nil
	}
	page := &statusPage{GeneratedAt: time.Now().UTC()}
	var description, homepage, logo, custom *string
	var verified *time.Time
	err := a.db.QueryRow(ctx, `
		SELECT p.id::text,p.account_id::text,p.slug,p.title,p.description,p.homepage_url,p.logo_url,p.theme,p.show_response_times,p.custom_domain,p.domain_verified_at
		FROM status_pages p JOIN accounts acc ON acc.id=p.account_id AND acc.status='active'
		WHERE p.deleted_at IS NULL AND p.published AND (($1<>'' AND p.slug=$1) OR ($2<>'' AND p.custom_domain=$2 AND p.domain_verified_at IS NOT NULL))`,
		slug, domain).Scan(&page.ID, &page.AccountID, &page.Slug, &page.Title, &description, &homepage, &logo, &page.Theme, &page.ShowResponseTimes, &custom, &verified)
	if err != nil {
		return nil, err
	}
	page.Description, page.HomepageURL, page.LogoURL, page.CustomDomain = deref(description), deref(homepage), deref(logo), deref(custom)
	page.DomainVerified = verified != nil
	if err = a.fillStatusComponents(ctx, page); err != nil {
		return nil, err
	}
	if err = a.fillStatusPosts(ctx, page); err != nil {
		return nil, err
	}
	page.Overall = overallStatus(page)
	a.statusPages.put(key, page)
	return page, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *App) fillStatusComponents(ctx context.Context, page *statusPage) error {
	rows, err := a.db.Query(ctx, `
		SELECT m.id::text, COALESCE(NULLIF(i.display_name,''), m.name), COALESCE(i.group_name,''), m.state, m.created_at,
			EXISTS (SELECT 1 FROM maintenance_windows w WHERE w.account_id=m.account_id AND w.deleted_at IS NULL
				AND now() >= w.starts_at AND now() < w.ends_at AND (cardinality(w.monitor_ids)=0 OR m.id = ANY(w.monitor_ids)))
		FROM status_page_items i JOIN monitors m ON m.id=i.monitor_id AND m.deleted_at IS NULL
		WHERE i.status_page_id=$1 ORDER BY i.position, m.created_at`, page.ID)
	if err != nil {
		return err
	}
	type component struct {
		item    statusItem
		created time.Time
	}
	var components []component
	var ids []string
	for rows.Next() {
		var c component
		var state string
		var inMaintenance bool
		if err = rows.Scan(&c.item.ID, &c.item.Name, &c.item.Group, &state, &c.created, &inMaintenance); err != nil {
			rows.Close()
			return err
		}
		switch {
		case inMaintenance:
			c.item.State, c.item.StateText = "maintenance", "Maintenance"
		case state == "up":
			c.item.State, c.item.StateText = "up", "Operational"
		case state == "down":
			c.item.State, c.item.StateText = "down", "Outage"
		case state == "paused":
			c.item.State, c.item.StateText = "unknown", "Paused"
		default:
			c.item.State, c.item.StateText = "unknown", "Checking"
		}
		components = append(components, c)
		ids = append(ids, c.item.ID)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(components) == 0 {
		return err
	}

	type dayKey struct{ monitor, day string }
	downtime := map[dayKey]float64{}
	incidents := map[dayKey]int{}
	rows, err = a.db.Query(ctx, `
		SELECT i.monitor_id::text, d::date::text,
			sum(extract(epoch FROM least(COALESCE(i.resolved_at, now()), d + interval '1 day') - greatest(i.started_at, d))), count(*)
		FROM incidents i, generate_series((now() AT TIME ZONE 'UTC')::date - $2::int + 1, (now() AT TIME ZONE 'UTC')::date, interval '1 day') AS d
		WHERE i.monitor_id::text = ANY($1) AND i.started_at < d + interval '1 day' AND COALESCE(i.resolved_at, now()) > d
		GROUP BY 1, 2`, ids, statusBarDays)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k dayKey
		var seconds float64
		var count int
		if err = rows.Scan(&k.monitor, &k.day, &seconds, &count); err != nil {
			rows.Close()
			return err
		}
		downtime[k], incidents[k] = seconds, count
	}
	rows.Close()

	responses := map[string]float64{}
	if page.ShowResponseTimes {
		rows, err = a.db.Query(ctx, `
			SELECT monitor_id::text, avg(total_ms) FROM check_results
			WHERE monitor_id::text = ANY($1) AND up AND total_ms IS NOT NULL AND checked_at > now() - interval '24 hours'
			GROUP BY 1`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var avg float64
			if err = rows.Scan(&id, &avg); err == nil {
				responses[id] = avg
			}
		}
		rows.Close()
	}

	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	for _, c := range components {
		item := c.item
		var observed, down float64
		for offset := statusBarDays - 1; offset >= 0; offset-- {
			day := today.AddDate(0, 0, -offset)
			label := day.Format("2 Jan 2006")
			end := day.Add(24 * time.Hour)
			if end.After(now) {
				end = now
			}
			start := day
			if c.created.After(start) {
				start = c.created
			}
			if !end.After(start) {
				item.Days = append(item.Days, statusBar{Tone: "", Title: label + " · No data"})
				continue
			}
			observed += end.Sub(start).Seconds()
			k := dayKey{item.ID, day.Format(time.DateOnly)}
			seconds := downtime[k]
			down += seconds
			switch {
			case seconds <= 0:
				item.Days = append(item.Days, statusBar{Tone: "ok", Title: label + " · No downtime"})
			default:
				tone := "warn"
				if seconds >= 30*60 {
					tone = "crit"
				}
				noun := "incident"
				if incidents[k] != 1 {
					noun = "incidents"
				}
				item.Days = append(item.Days, statusBar{Tone: tone, Title: fmt.Sprintf("%s · %d %s, %s down", label, incidents[k], noun, notify.HumanDuration(time.Duration(seconds)*time.Second))})
			}
		}
		item.UptimeValue = 100
		if observed > 0 {
			item.UptimeValue = 100 * (1 - down/observed)
		}
		item.Uptime = formatUptime(item.UptimeValue)
		if avg, ok := responses[item.ID]; ok {
			item.ResponseMs = fmt.Sprintf("%.0f ms", avg)
		}
		if len(page.Groups) == 0 || page.Groups[len(page.Groups)-1].Name != item.Group {
			page.Groups = append(page.Groups, statusGroup{Name: item.Group})
		}
		page.Groups[len(page.Groups)-1].Items = append(page.Groups[len(page.Groups)-1].Items, item)
	}
	return nil
}

func formatUptime(value float64) string {
	switch {
	case value >= 100:
		return "100%"
	case value >= 99.99:
		return fmt.Sprintf("%.3f%%", value)
	}
	return fmt.Sprintf("%.2f%%", value)
}

func (a *App) fillStatusPosts(ctx context.Context, page *statusPage) error {
	rows, err := a.db.Query(ctx, `
		SELECT p.id::text,p.kind,p.impact,p.title,p.status,p.starts_at,p.ends_at,p.resolved_at,
			COALESCE((SELECT jsonb_agg(jsonb_build_object('status',u.status,'body',u.body,'at',u.created_at) ORDER BY u.created_at DESC)
				FROM status_post_updates u WHERE u.status_post_id=p.id),'[]')
		FROM status_posts p
		WHERE p.status_page_id=$1 AND p.deleted_at IS NULL
		  AND (p.resolved_at IS NULL OR p.resolved_at > now() - make_interval(days => $2) OR p.starts_at > now() - make_interval(days => $2))
		ORDER BY p.starts_at DESC LIMIT 100`, page.ID, statusHistoryDays)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := time.Now().UTC()
	byDay := map[string][]statusPost{}
	for rows.Next() {
		var post statusPost
		var ends *time.Time
		var updates []struct {
			Status string    `json:"status"`
			Body   string    `json:"body"`
			At     time.Time `json:"at"`
		}
		if err = rows.Scan(&post.ID, &post.Kind, &post.Impact, &post.Title, &post.Status, &post.StartsAt, &ends, &post.ResolvedAt, &updates); err != nil {
			return err
		}
		for _, u := range updates {
			post.Updates = append(post.Updates, statusUpdate{Status: humanStatus(u.Status), When: statusTime(u.At), Body: u.Body})
		}
		if len(updates) > 0 {
			post.Summary = updates[0].Body
			if len(post.Summary) > 280 {
				post.Summary = post.Summary[:280] + "…"
			}
		}
		post.StatusText = humanStatus(post.Status)
		switch {
		case post.Kind == "maintenance" && ends != nil:
			post.When = "Scheduled " + statusTime(post.StartsAt) + " – " + statusTime(*ends)
		case post.ResolvedAt != nil:
			post.When = statusTime(post.StartsAt) + " – " + statusTime(*post.ResolvedAt)
		default:
			post.When = "Since " + statusTime(post.StartsAt)
		}
		active := post.ResolvedAt == nil && (post.Kind == "incident" || ends == nil || ends.After(now))
		if active {
			page.Active = append(page.Active, post)
			continue
		}
		day := post.StartsAt.UTC().Format("2 Jan 2006")
		byDay[day] = append(byDay[day], post)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	today := now.Truncate(24 * time.Hour)
	for offset := range statusHistoryDays {
		day := today.AddDate(0, 0, -offset).Format("2 Jan 2006")
		if len(byDay[day]) > 0 {
			page.History = append(page.History, statusDay{Date: day, Posts: byDay[day]})
		}
	}
	return nil
}

func overallStatus(page *statusPage) statusOverall {
	total, down, maintenance := 0, 0, 0
	for _, group := range page.Groups {
		for _, item := range group.Items {
			total++
			switch item.State {
			case "down":
				down++
			case "maintenance":
				maintenance++
			}
		}
	}
	severity := 0
	for _, post := range page.Active {
		switch {
		case post.Kind == "incident" && (post.Impact == "critical" || post.Impact == "major"):
			severity = max(severity, 3)
		case post.Kind == "incident":
			severity = max(severity, 2)
		case post.Kind == "maintenance" && post.Status == "in_progress":
			severity = max(severity, 1)
		}
	}
	switch {
	case down > 0 && down*2 > total, severity == 3:
		return statusOverall{Tone: "crit", Text: "Major outage", Indicator: "major"}
	case down > 0, severity == 2:
		return statusOverall{Tone: "warn", Text: "Partial outage", Indicator: "minor"}
	case maintenance > 0, severity == 1:
		return statusOverall{Tone: "maint", Text: "Under maintenance", Indicator: "maintenance"}
	}
	return statusOverall{Tone: "ok", Text: "All systems operational", Indicator: "none"}
}

func (a *App) statusPageURL(slug, domain string, verified bool) string {
	if domain != "" && verified {
		return "https://" + domain
	}
	if a.statusOriginIsSeparate() {
		return strings.TrimRight(a.cfg.StatusOrigin, "/") + "/" + slug
	}
	return strings.TrimRight(a.cfg.AppOrigin, "/") + "/status/" + slug
}

func (a *App) statusOriginIsSeparate() bool {
	host := originHost(a.cfg.StatusOrigin)
	return host != "" && host != originHost(a.cfg.AppOrigin) && host != originHost(a.cfg.APIOrigin) && host != originHost(a.cfg.DashboardOrigin)
}

func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func originHostname(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (a *App) firstPartyHost(r *http.Request) bool {
	return httpx.HostMatches(r, a.cfg.AppOrigin, a.cfg.APIOrigin, a.cfg.DashboardOrigin, a.cfg.StatusOrigin)
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func (a *App) serveStatusHost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if a.statusOriginIsSeparate() && httpx.HostMatches(r, a.cfg.StatusOrigin) {
		slug, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if slug == "" {
			http.Redirect(w, r, a.cfg.AppOrigin, http.StatusFound)
			return true
		}
		a.renderStatus(w, r, slug, "", rest)
		return true
	}
	if a.firstPartyHost(r) {
		return false
	}
	a.renderStatus(w, r, "", requestHost(r), strings.TrimPrefix(r.URL.Path, "/"))
	return true
}

func (a *App) statusOnAppOrigin(w http.ResponseWriter, r *http.Request) {
	a.renderStatus(w, r, r.PathValue("slug"), "", r.PathValue("rest"))
}

func (a *App) renderStatus(w http.ResponseWriter, r *http.Request, slug, domain, rest string) {
	if (slug != "" && !slugPattern.MatchString(slug)) || (rest != "" && rest != "status.json" && rest != "badge.svg") {
		http.NotFound(w, r)
		return
	}
	page, err := a.loadStatusPage(r.Context(), slug, domain)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.logError("status page load failed", "status_page", "load_status_page", err, "slug", slug, "domain", domain)
		http.Error(w, "This status page is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	base := a.statusPageURL(page.Slug, page.CustomDomain, page.DomainVerified && domain != "")
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Header().Set("X-Robots-Tag", "all")
	switch rest {
	case "status.json":
		w.Header().Set("Access-Control-Allow-Origin", "*")
		httpx.WriteJSON(w, http.StatusOK, statusJSON(page, base))
	case "badge.svg":
		a.writeBadge(w, r, page.Overall)
	default:
		a.writeStatusHTML(w, r, page, base)
	}
}

func statusJSON(page *statusPage, base string) map[string]any {
	components := make([]map[string]any, 0)
	for _, group := range page.Groups {
		for _, item := range group.Items {
			components = append(components, map[string]any{
				"id": item.ID, "name": item.Name, "group": nullIfEmpty(item.Group), "status": item.State, "uptime_90d": item.UptimeValue,
			})
		}
	}
	active := make([]map[string]any, 0)
	for _, post := range page.Active {
		active = append(active, map[string]any{"id": post.ID, "kind": post.Kind, "title": post.Title, "status": post.Status, "impact": post.Impact, "starts_at": post.StartsAt})
	}
	return map[string]any{
		"page":       map[string]any{"title": page.Title, "url": base},
		"status":     map[string]any{"indicator": page.Overall.Indicator, "description": page.Overall.Text},
		"components": components,
		"notices":    active,
		"updated_at": page.GeneratedAt.Format(time.RFC3339),
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *App) writeStatusHTML(w http.ResponseWriter, r *http.Request, page *statusPage, base string) {
	view := map[string]any{
		"Title": page.Title, "Description": page.Description, "HomepageURL": page.HomepageURL, "LogoURL": page.LogoURL,
		"Theme": page.Theme, "Overall": page.Overall, "Active": page.Active, "Groups": page.Groups, "History": page.History,
		"Updated": page.GeneratedAt.Format("15:04 UTC"), "PageURL": base,
		"JSONURL": strings.TrimRight(base, "/") + "/status.json", "BadgeURL": strings.TrimRight(base, "/") + "/badge.svg",
		"PoweredBy": strings.TrimRight(a.cfg.AppOrigin, "/") + "/?ref=status", "HistoryDays": statusHistoryDays,
	}
	var out bytes.Buffer
	if err := statusPageTemplate.Execute(&out, view); err != nil {
		a.logError("status page render failed", "status_page", "render_status_page", err, "status_page_id", page.ID)
		http.Error(w, "This status page is temporarily unavailable.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src https: data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(out.Bytes())
}

func (a *App) writeBadge(w http.ResponseWriter, r *http.Request, overall statusOverall) {
	value := map[string]string{"none": "operational", "minor": "partial outage", "major": "major outage", "maintenance": "maintenance"}[overall.Indicator]
	color := map[string]string{"ok": "#0ca30c", "warn": "#e09400", "crit": "#d03b3b", "maint": "#2a78d6"}[overall.Tone]
	label := "status"
	labelWidth, valueWidth := 6*len(label)+12, 6*len(value)+12
	var out bytes.Buffer
	if err := badgeTemplate.Execute(&out, map[string]any{
		"Label": label, "Value": value, "Color": color, "LabelWidth": labelWidth, "ValueWidth": valueWidth,
		"Width": labelWidth + valueWidth, "LabelCenter": labelWidth / 2, "ValueCenter": labelWidth + valueWidth/2,
	}); err != nil {
		http.Error(w, "badge unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=60")
	if r.Method != http.MethodHead {
		_, _ = w.Write(out.Bytes())
	}
}

func (a *App) domainAsk(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSuffix(strings.ToLower(r.URL.Query().Get("domain")), ".")
	if domain != "" && domain == originHostname(a.cfg.StatusOrigin) {
		w.WriteHeader(http.StatusOK)
		return
	}
	var exists bool
	if !hostnamePattern.MatchString(domain) {
		http.NotFound(w, r)
		return
	}
	if err := a.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM status_pages WHERE custom_domain=$1 AND domain_verified_at IS NOT NULL AND deleted_at IS NULL)`, domain).Scan(&exists); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type statusPageInput struct {
	Title             *string  `json:"title"`
	Slug              *string  `json:"slug"`
	Description       *string  `json:"description"`
	HomepageURL       *string  `json:"homepage_url"`
	LogoURL           *string  `json:"logo_url"`
	Theme             *string  `json:"theme"`
	ShowResponseTimes *bool    `json:"show_response_times"`
	Published         *bool    `json:"published"`
	CustomDomain      *string  `json:"custom_domain"`
	MonitorIDs        []string `json:"monitor_ids"`
}

type statusPageRow struct {
	ID, Slug, Title, Theme                          string
	Description, HomepageURL, LogoURL, CustomDomain *string
	ShowResponseTimes, Published                    bool
	DomainVerifiedAt                                *time.Time
	CreatedAt                                       time.Time
	Items                                           int
}

const statusPageColumns = `p.id::text,p.slug,p.title,p.theme,p.description,p.homepage_url,p.logo_url,p.custom_domain,p.show_response_times,p.published,
	p.domain_verified_at,p.created_at,(SELECT count(*) FROM status_page_items i WHERE i.status_page_id=p.id)`

func scanStatusPage(row pgx.Row) (statusPageRow, error) {
	var p statusPageRow
	err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Theme, &p.Description, &p.HomepageURL, &p.LogoURL, &p.CustomDomain, &p.ShowResponseTimes,
		&p.Published, &p.DomainVerifiedAt, &p.CreatedAt, &p.Items)
	return p, err
}

func (a *App) statusPageView(p statusPageRow) map[string]any {
	view := map[string]any{
		"id": p.ID, "slug": p.Slug, "title": p.Title, "theme": p.Theme, "description": p.Description, "homepage_url": p.HomepageURL,
		"logo_url": p.LogoURL, "show_response_times": p.ShowResponseTimes, "published": p.Published, "items": p.Items,
		"created_at": httpx.RFC3339(&p.CreatedAt), "public_url": a.statusPageURL(p.Slug, deref(p.CustomDomain), p.DomainVerifiedAt != nil),
		"custom_domain": nil,
	}
	if p.CustomDomain != nil {
		view["custom_domain"] = map[string]any{
			"hostname": *p.CustomDomain, "verified": p.DomainVerifiedAt != nil, "verified_at": httpx.RFC3339(p.DomainVerifiedAt),
			"cname_target": a.cnameTarget(),
		}
	}
	return view
}

func (a *App) cnameTarget() string {
	if a.statusOriginIsSeparate() {
		return originHostname(a.cfg.StatusOrigin)
	}
	return originHostname(a.cfg.AppOrigin)
}

func invalidStatusPage(message string) httpx.Error {
	return httpx.Error{Code: "invalid_status_page", Message: message, Status: http.StatusUnprocessableEntity}
}

func optionalHTTPS(field string, raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(value) > 2048 {
		return nil, invalidStatusPage(field + " must be an https:// address.")
	}
	return &value, nil
}

func slugFrom(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if len(slug) < 3 {
		slug += "-status"
	}
	return slug
}

func (a *App) listStatusPages(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+statusPageColumns+` FROM status_pages p WHERE p.account_id=$1 AND p.deleted_at IS NULL ORDER BY p.created_at`, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanStatusPage(rows)
			if err != nil {
				return err
			}
			items = append(items, a.statusPageView(row))
		}
		return rows.Err()
	}); err != nil {
		writeMonitorError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) createStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	var in statusPageInput
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	if in.Title == nil {
		httpx.WriteError(w, r, invalidStatusPage("title is required."))
		return
	}
	if in.Slug == nil {
		slug := slugFrom(*in.Title)
		in.Slug = &slug
	}
	limits := a.planLimits(p.Plan)
	var created statusPageRow
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM status_pages WHERE account_id=$1 AND deleted_at IS NULL`, p.AccountID.String()).Scan(&count); err != nil {
			return err
		}
		if count >= limits.MaxStatusPages {
			return errPlanLimit
		}
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO status_pages(account_id,slug,title) VALUES($1,$2,'untitled') RETURNING id::text`,
			p.AccountID.String(), "pending-"+strings.ReplaceAll(uuid.NewV7().String(), "-", "")[:24]).Scan(&id); err != nil {
			return err
		}
		if err := a.applyStatusPageInput(r, tx, id, p.AccountID.String(), limits, in); err != nil {
			return err
		}
		if in.MonitorIDs != nil {
			items := make([]statusItemInput, 0, len(in.MonitorIDs))
			for _, monitorIDValue := range in.MonitorIDs {
				items = append(items, statusItemInput{MonitorID: monitorIDValue})
			}
			if err := replaceStatusItems(r, tx, id, p.AccountID.String(), items); err != nil {
				return err
			}
		}
		var err error
		created, err = scanStatusPage(tx.QueryRow(r.Context(), `SELECT `+statusPageColumns+` FROM status_pages p WHERE p.id=$1`, id))
		return err
	})
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": a.statusPageView(created)})
}

func writeStatusPageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errSlugTaken) {
		httpx.WriteError(w, r, httpx.Error{Code: "slug_taken", Message: "That address is already taken; choose another slug.", Status: http.StatusConflict})
		return
	}
	if errors.Is(err, errDomainTaken) {
		httpx.WriteError(w, r, httpx.Error{Code: "domain_taken", Message: "That domain is already connected to a status page.", Status: http.StatusConflict})
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, httpx.Error{Code: "status_page_not_found", Message: "The requested status page was not found.", Status: http.StatusNotFound})
		return
	}
	writeMonitorError(w, r, err)
}

var (
	errSlugTaken   = errors.New("slug taken")
	errDomainTaken = errors.New("domain taken")
)

func (a *App) applyStatusPageInput(r *http.Request, tx pgx.Tx, id, accountID string, limits usage.Limits, in statusPageInput) error {
	set := map[string]any{}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if n := len([]rune(title)); n < 1 || n > 128 {
			return invalidStatusPage("title must be 1 to 128 characters.")
		}
		set["title"] = title
	}
	if in.Slug != nil {
		slug := strings.ToLower(strings.TrimSpace(*in.Slug))
		if !slugPattern.MatchString(slug) {
			return invalidStatusPage("slug must be 3 to 48 lowercase letters, digits or dashes, starting and ending with a letter or digit.")
		}
		var taken bool
		if err := a.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM status_pages WHERE slug=$1 AND id<>$2 AND deleted_at IS NULL)`, slug, id).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return errSlugTaken
		}
		set["slug"] = slug
	}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		if len([]rune(description)) > 512 {
			return invalidStatusPage("description must be at most 512 characters.")
		}
		set["description"] = nilIfBlank(description)
	}
	for field, raw := range map[string]*string{"homepage_url": in.HomepageURL, "logo_url": in.LogoURL} {
		if raw == nil {
			continue
		}
		value, err := optionalHTTPS(field, raw)
		if err != nil {
			return err
		}
		set[field] = value
	}
	if in.Theme != nil {
		if !slices.Contains([]string{"auto", "light", "dark"}, *in.Theme) {
			return invalidStatusPage("theme must be auto, light or dark.")
		}
		set["theme"] = *in.Theme
	}
	if in.ShowResponseTimes != nil {
		set["show_response_times"] = *in.ShowResponseTimes
	}
	if in.Published != nil {
		set["published"] = *in.Published
	}
	if in.CustomDomain != nil {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(*in.CustomDomain)), ".")
		if domain == "" {
			set["custom_domain"], set["domain_verified_at"] = nil, nil
		} else {
			if limits.MaxCustomDomains == 0 {
				return httpx.Error{Code: "plan_limit_exceeded", Message: "Custom domains are available on paid plans.", Status: http.StatusTooManyRequests}
			}
			if !hostnamePattern.MatchString(domain) || len(domain) > 253 {
				return invalidStatusPage("custom_domain must be a hostname such as status.example.com.")
			}
			if domain == originHostname(a.cfg.StatusOrigin) || domain == originHostname(a.cfg.AppOrigin) || strings.HasSuffix(domain, "."+originHostname(a.cfg.AppOrigin)) {
				return invalidStatusPage("Use a domain you own.")
			}
			var current *string
			var others int
			if err := tx.QueryRow(r.Context(), `SELECT custom_domain,(SELECT count(*) FROM status_pages WHERE account_id=$2 AND id<>$1 AND custom_domain IS NOT NULL AND deleted_at IS NULL) FROM status_pages WHERE id=$1`, id, accountID).Scan(&current, &others); err != nil {
				return err
			}
			if current == nil || *current != domain {
				if others >= limits.MaxCustomDomains {
					return httpx.Error{Code: "plan_limit_exceeded", Message: fmt.Sprintf("Your plan connects at most %d custom domain(s).", limits.MaxCustomDomains), Status: http.StatusTooManyRequests}
				}
				var taken bool
				if err := a.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM status_pages WHERE custom_domain=$1 AND id<>$2 AND deleted_at IS NULL)`, domain, id).Scan(&taken); err != nil {
					return err
				}
				if taken {
					return errDomainTaken
				}
				set["custom_domain"], set["domain_verified_at"] = domain, nil
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	columns := make([]string, 0, len(set))
	args := []any{id, accountID}
	for _, column := range slices.Sorted(func(yield func(string) bool) {
		for k := range set {
			if !yield(k) {
				return
			}
		}
	}) {
		args = append(args, set[column])
		columns = append(columns, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	_, err := tx.Exec(r.Context(), `UPDATE status_pages SET `+strings.Join(columns, ",")+`,updated_at=now() WHERE id=$1 AND account_id=$2`, args...)
	return err
}

func nilIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func statusPageID(r *http.Request, name string) (string, bool) {
	id := r.PathValue(name)
	_, err := uuid.Parse(id)
	return id, err == nil
}

func (a *App) getStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	a.writeStatusPageDetail(w, r, p.AccountID.String(), id)
}

func (a *App) writeStatusPageDetail(w http.ResponseWriter, r *http.Request, accountID, id string) {
	var view map[string]any
	if err := a.readTenant(r.Context(), accountID, func(tx pgx.Tx) error {
		row, err := scanStatusPage(tx.QueryRow(r.Context(), `SELECT `+statusPageColumns+` FROM status_pages p WHERE p.id=$1 AND p.account_id=$2 AND p.deleted_at IS NULL`, id, accountID))
		if err != nil {
			return err
		}
		view = a.statusPageView(row)
		rows, err := tx.Query(r.Context(), `
			SELECT i.monitor_id::text,m.name,COALESCE(i.display_name,''),COALESCE(i.group_name,''),m.state
			FROM status_page_items i JOIN monitors m ON m.id=i.monitor_id AND m.deleted_at IS NULL
			WHERE i.status_page_id=$1 ORDER BY i.position`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var monitorIDValue, name, display, group, state string
			if err = rows.Scan(&monitorIDValue, &name, &display, &group, &state); err != nil {
				return err
			}
			items = append(items, map[string]any{"monitor_id": monitorIDValue, "monitor_name": name, "display_name": nullIfEmpty(display), "group_name": nullIfEmpty(group), "state": state})
		}
		view["components"] = items
		return rows.Err()
	}); err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": view})
}

func (a *App) updateStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	var in statusPageInput
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	var updated statusPageRow
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT true FROM status_pages WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, p.AccountID.String()).Scan(&exists); err != nil {
			return err
		}
		if err := a.applyStatusPageInput(r, tx, id, p.AccountID.String(), a.planLimits(p.Plan), in); err != nil {
			return err
		}
		var err error
		updated, err = scanStatusPage(tx.QueryRow(r.Context(), `SELECT `+statusPageColumns+` FROM status_pages p WHERE p.id=$1`, id))
		return err
	})
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": a.statusPageView(updated)})
}

func (a *App) deleteStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE status_pages SET deleted_at=now(),custom_domain=NULL,domain_verified_at=NULL,updated_at=now() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String())
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	if affected == 0 {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	a.statusPages.clear()
	w.WriteHeader(http.StatusNoContent)
}

type statusItemInput struct {
	MonitorID   string  `json:"monitor_id"`
	DisplayName *string `json:"display_name"`
	GroupName   *string `json:"group_name"`
}

func replaceStatusItems(r *http.Request, tx pgx.Tx, pageID, accountID string, items []statusItemInput) error {
	if len(items) > maxStatusItems {
		return invalidStatusPage(fmt.Sprintf("A status page lists at most %d components.", maxStatusItems))
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM status_page_items WHERE status_page_id=$1`, pageID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for position, item := range items {
		if _, err := uuid.Parse(item.MonitorID); err != nil || seen[item.MonitorID] {
			return invalidStatusPage("Each component must name a distinct monitor.")
		}
		seen[item.MonitorID] = true
		display, group := strings.TrimSpace(deref(item.DisplayName)), strings.TrimSpace(deref(item.GroupName))
		if len([]rune(display)) > 128 || len([]rune(group)) > 128 {
			return invalidStatusPage("display_name and group_name must be at most 128 characters.")
		}
		tag, err := tx.Exec(r.Context(), `
			INSERT INTO status_page_items(status_page_id,monitor_id,account_id,group_name,display_name,position)
			SELECT $1,m.id,$2,$3,$4,$5 FROM monitors m WHERE m.id=$6 AND m.account_id=$2 AND m.deleted_at IS NULL`,
			pageID, accountID, nilIfBlank(group), nilIfBlank(display), position, item.MonitorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return invalidStatusPage("A component names a monitor that does not exist.")
		}
	}
	return nil
}

func (a *App) putStatusItems(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	var in struct {
		Items []statusItemInput `json:"items"`
	}
	body, err := readBody(w, r, 128<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT true FROM status_pages WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, p.AccountID.String()).Scan(&exists); err != nil {
			return err
		}
		return replaceStatusItems(r, tx, id, p.AccountID.String(), in.Items)
	})
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	a.writeStatusPageDetail(w, r, p.AccountID.String(), id)
}

func (a *App) verifyStatusDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	var domain *string
	if err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT custom_domain FROM status_pages WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`,
		[]any{id, p.AccountID.String()}, &domain); err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	if domain == nil {
		httpx.WriteError(w, r, invalidStatusPage("Connect a custom domain first."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	target := a.cnameTarget()
	cname, err := a.lookupCNAME(ctx, *domain)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(cname, "."), target) {
		found := "no CNAME record"
		if err == nil && cname != "" {
			found = "a CNAME to " + strings.TrimSuffix(cname, ".")
		}
		httpx.WriteError(w, r, httpx.Error{Code: "domain_not_verified", Message: fmt.Sprintf("%s has %s. Point a CNAME record at %s, wait for DNS to update, then try again.", *domain, found, target), Status: http.StatusConflict})
		return
	}
	if _, err = a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE status_pages SET domain_verified_at=now(),updated_at=now() WHERE id=$1 AND account_id=$2`, id, p.AccountID.String()); err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	a.writeStatusPageDetail(w, r, p.AccountID.String(), id)
}

func (a *App) lookupCNAME(ctx context.Context, host string) (string, error) {
	if a.resolveCNAME != nil {
		return a.resolveCNAME(ctx, host)
	}
	return net.DefaultResolver.LookupCNAME(ctx, host)
}

type statusPostInput struct {
	Kind     string     `json:"kind"`
	Title    string     `json:"title"`
	Impact   string     `json:"impact"`
	Status   string     `json:"status"`
	Body     string     `json:"body"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

func (a *App) listStatusPosts(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT true FROM status_pages WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String()).Scan(&exists); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT p.id::text,p.kind,p.title,p.impact,p.status,p.starts_at,p.ends_at,p.resolved_at,p.created_at,
				COALESCE((SELECT jsonb_agg(jsonb_build_object('id',u.id,'status',u.status,'body',u.body,'created_at',u.created_at) ORDER BY u.created_at DESC)
					FROM status_post_updates u WHERE u.status_post_id=p.id),'[]')
			FROM status_posts p WHERE p.status_page_id=$1 AND p.account_id=$2 AND p.deleted_at IS NULL
			ORDER BY p.starts_at DESC LIMIT 100`, id, p.AccountID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var postID, kind, title, impact, status string
			var starts, created time.Time
			var ends, resolved *time.Time
			var updates []map[string]any
			if err = rows.Scan(&postID, &kind, &title, &impact, &status, &starts, &ends, &resolved, &created, &updates); err != nil {
				return err
			}
			items = append(items, map[string]any{
				"id": postID, "kind": kind, "title": title, "impact": impact, "status": status, "starts_at": httpx.RFC3339(&starts),
				"ends_at": httpx.RFC3339(ends), "resolved_at": httpx.RFC3339(resolved), "created_at": httpx.RFC3339(&created), "updates": updates,
			})
		}
		return rows.Err()
	}); err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func validPostStatus(kind, status string) bool {
	return slices.Contains(postStatuses[kind], status)
}

func closingStatus(status string) bool { return status == "resolved" || status == "completed" }

func (a *App) createStatusPost(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id, valid := statusPageID(r, "id")
	if !valid {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	var in statusPostInput
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if in.Impact == "" {
		in.Impact = "minor"
	}
	if in.Status == "" && len(postStatuses[in.Kind]) > 0 {
		in.Status = postStatuses[in.Kind][0]
	}
	switch {
	case postStatuses[in.Kind] == nil:
		httpx.WriteError(w, r, invalidStatusPage("kind must be incident or maintenance."))
		return
	case !validPostStatus(in.Kind, in.Status):
		httpx.WriteError(w, r, invalidStatusPage(fmt.Sprintf("A %s's status is one of %s.", in.Kind, strings.Join(postStatuses[in.Kind], ", "))))
		return
	case !slices.Contains([]string{"none", "minor", "major", "critical"}, in.Impact):
		httpx.WriteError(w, r, invalidStatusPage("impact must be none, minor, major or critical."))
		return
	case len([]rune(in.Title)) < 1 || len([]rune(in.Title)) > 256:
		httpx.WriteError(w, r, invalidStatusPage("title must be 1 to 256 characters."))
		return
	case len([]rune(in.Body)) < 1 || len([]rune(in.Body)) > 5000:
		httpx.WriteError(w, r, invalidStatusPage("body must be 1 to 5000 characters."))
		return
	case in.Kind == "maintenance" && (in.StartsAt == nil || in.EndsAt == nil || !in.EndsAt.After(*in.StartsAt)):
		httpx.WriteError(w, r, invalidStatusPage("Maintenance needs starts_at and a later ends_at."))
		return
	}
	starts := time.Now().UTC()
	if in.StartsAt != nil {
		starts = *in.StartsAt
	}
	var postID string
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT true FROM status_pages WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, p.AccountID.String()).Scan(&exists); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO status_posts(status_page_id,account_id,kind,title,impact,status,starts_at,ends_at,resolved_at,author_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,CASE WHEN $9 THEN now() END,$10) RETURNING id::text`,
			id, p.AccountID.String(), in.Kind, in.Title, in.Impact, in.Status, starts, in.EndsAt, closingStatus(in.Status), p.PrincipalID.String()).Scan(&postID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO status_post_updates(status_post_id,account_id,status,body,author_id) VALUES($1,$2,$3,$4,$5)`,
			postID, p.AccountID.String(), in.Status, in.Body, p.PrincipalID.String())
		return err
	})
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": postID}})
}

func (a *App) addStatusPostUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	pageID, validPage := statusPageID(r, "id")
	postID, validPost := statusPageID(r, "post")
	if !validPage || !validPost {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	var in struct {
		Status string `json:"status"`
		Body   string `json:"body"`
	}
	body, err := readBody(w, r, 64<<10)
	if err != nil || decode(body, &in) != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_json"))
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if n := len([]rune(in.Body)); n < 1 || n > 5000 {
		httpx.WriteError(w, r, invalidStatusPage("body must be 1 to 5000 characters."))
		return
	}
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(r.Context(), `SELECT kind FROM status_posts WHERE id=$1 AND status_page_id=$2 AND account_id=$3 AND deleted_at IS NULL FOR UPDATE`,
			postID, pageID, p.AccountID.String()).Scan(&kind); err != nil {
			return err
		}
		if !validPostStatus(kind, in.Status) {
			return invalidStatusPage(fmt.Sprintf("A %s's status is one of %s.", kind, strings.Join(postStatuses[kind], ", ")))
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE status_posts SET status=$2,resolved_at=CASE WHEN $3 THEN COALESCE(resolved_at,now()) ELSE NULL END WHERE id=$1`,
			postID, in.Status, closingStatus(in.Status)); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO status_post_updates(status_post_id,account_id,status,body,author_id) VALUES($1,$2,$3,$4,$5)`,
			postID, p.AccountID.String(), in.Status, in.Body, p.PrincipalID.String())
		return err
	})
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	a.statusPages.clear()
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": postID, "status": in.Status}})
}

func (a *App) deleteStatusPost(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authWorkspaceManager(w, r)
	if !ok {
		return
	}
	pageID, validPage := statusPageID(r, "id")
	postID, validPost := statusPageID(r, "post")
	if !validPage || !validPost {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	affected, err := a.tenantExec(r.Context(), p.AccountID.String(),
		`UPDATE status_posts SET deleted_at=now() WHERE id=$1 AND status_page_id=$2 AND account_id=$3 AND deleted_at IS NULL`, postID, pageID, p.AccountID.String())
	if err != nil {
		writeStatusPageError(w, r, err)
		return
	}
	if affected == 0 {
		writeStatusPageError(w, r, pgx.ErrNoRows)
		return
	}
	a.statusPages.clear()
	w.WriteHeader(http.StatusNoContent)
}
