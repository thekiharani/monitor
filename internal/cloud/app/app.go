package app

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/billing"
	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/email"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/probe"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

type App struct {
	cfg          config.Config
	plans        *usage.Catalog
	db           *pgxpool.Pool
	tenant       *pgxpool.Pool
	mail         email.Sender
	billing      billing.Provider
	log          *slog.Logger
	limiters     *limiterSet
	metrics      *appMetrics
	emailWake    chan struct{}
	alertWake    chan struct{}
	statusPages  *statusCache
	resolveCNAME func(context.Context, string) (string, error)
	stop         chan struct{}
	jobs         sync.WaitGroup
	dashboard    *dashboardHandler
	control      *control.Service
}

type Option func(*App)

func WithDashboard(files fs.FS) Option {
	return func(a *App) { a.dashboard = newDashboardHandler(files, a.cfg.AppOrigin) }
}

func New(c config.Config, db, tenant *pgxpool.Pool, sender email.Sender, logger *slog.Logger, options ...Option) *App {
	provider := paystack.New(c)
	a := &App{
		cfg:         c,
		plans:       usage.NewCatalog(c),
		db:          db,
		tenant:      tenant,
		mail:        sender,
		billing:     provider,
		log:         logger,
		limiters:    newLimiterSet(),
		metrics:     newAppMetrics(),
		emailWake:   make(chan struct{}, 1),
		alertWake:   make(chan struct{}, 1),
		statusPages: newStatusCache(),
		stop:        make(chan struct{}),
	}
	a.control = control.New(db, provider, c, a.plans)
	for _, option := range options {
		option(a)
	}
	return a
}

func (a *App) Start(ctx context.Context) {
	a.publishExpvar()
	a.jobs.Go(func() { a.plansWorker(ctx) })
	a.jobs.Go(func() { a.cleanupWorker(ctx) })
	a.jobs.Go(func() { a.emailWorker(ctx) })
	a.jobs.Go(func() { a.billingWorker(ctx) })
	a.jobs.Go(func() { a.metricsWorker(ctx) })
	a.jobs.Go(func() { a.maintainResults(ctx) })
	a.jobs.Go(func() { a.probeWorker(ctx) })
	a.jobs.Go(func() { a.alertWorker(ctx) })
	a.jobs.Go(func() { a.heartbeatWorker(ctx) })
	a.jobs.Go(func() { a.probeWatchWorker(ctx) })
}

func (a *App) Stop(ctx context.Context) {
	close(a.stop)
	done := make(chan struct{})
	go func() { a.jobs.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (a *App) Handler() http.Handler {
	mux := a.routes()
	guard := a.crossOriginGuard()
	return a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := guard.Check(r); err != nil {
			httpx.WriteError(w, r, httpx.NewError("cross_origin"))
			return
		}
		if r.URL.Path != "/internal/domains/ask" && r.URL.Path != "/health/live" && r.URL.Path != "/health/ready" && a.serveStatusHost(w, r) {
			return
		}
		if destination, ok := a.signedInAtLoginPage(r); ok {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, destination, http.StatusSeeOther)
			return
		}
		if a.dashboard != nil && httpx.HostMatches(r, a.cfg.DashboardOrigin) && a.dashboard.handles(r.URL.Path) {
			a.dashboard.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func (a *App) crossOriginGuard() *http.CrossOriginProtection {
	guard := http.NewCrossOriginProtection()
	for _, origin := range a.cfg.CORSAllowedOrigins {
		if err := guard.AddTrustedOrigin(origin); err != nil {
			a.logError("trusted origin rejected", "startup", "configure_cross_origin_guard", err, "origin", origin)
		}
	}
	return guard
}

func (a *App) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/otp/request", a.requestOTP)
	mux.HandleFunc("POST /auth/otp/verify", a.verifyOTP)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /api/dashboard/v1/me", a.dashboardMe)
	mux.HandleFunc("PATCH /api/dashboard/v1/me", a.updateProfile)
	mux.HandleFunc("POST /api/dashboard/v1/me/email", a.requestEmailChange)
	mux.HandleFunc("POST /api/dashboard/v1/me/email/verify", a.verifyEmailChange)
	mux.HandleFunc("PATCH /api/dashboard/v1/account", a.updateWorkspace)
	mux.HandleFunc("GET /api/dashboard/v1/usage", a.usage)
	mux.HandleFunc("GET /api/dashboard/v1/status-pages", a.listStatusPages)
	mux.HandleFunc("POST /api/dashboard/v1/status-pages", a.createStatusPage)
	mux.HandleFunc("GET /api/dashboard/v1/status-pages/{id}", a.getStatusPage)
	mux.HandleFunc("PATCH /api/dashboard/v1/status-pages/{id}", a.updateStatusPage)
	mux.HandleFunc("DELETE /api/dashboard/v1/status-pages/{id}", a.deleteStatusPage)
	mux.HandleFunc("PUT /api/dashboard/v1/status-pages/{id}/components", a.putStatusItems)
	mux.HandleFunc("POST /api/dashboard/v1/status-pages/{id}/domain/verify", a.verifyStatusDomain)
	mux.HandleFunc("GET /api/dashboard/v1/status-pages/{id}/posts", a.listStatusPosts)
	mux.HandleFunc("POST /api/dashboard/v1/status-pages/{id}/posts", a.createStatusPost)
	mux.HandleFunc("POST /api/dashboard/v1/status-pages/{id}/posts/{post}/updates", a.addStatusPostUpdate)
	mux.HandleFunc("DELETE /api/dashboard/v1/status-pages/{id}/posts/{post}", a.deleteStatusPost)
	mux.HandleFunc("GET /api/v1/status-pages", a.listStatusPages)
	mux.HandleFunc("POST /api/v1/status-pages", a.createStatusPage)
	mux.HandleFunc("GET /api/v1/status-pages/{id}", a.getStatusPage)
	mux.HandleFunc("PATCH /api/v1/status-pages/{id}", a.updateStatusPage)
	mux.HandleFunc("DELETE /api/v1/status-pages/{id}", a.deleteStatusPage)
	mux.HandleFunc("PUT /api/v1/status-pages/{id}/components", a.putStatusItems)
	mux.HandleFunc("POST /api/v1/status-pages/{id}/domain/verify", a.verifyStatusDomain)
	mux.HandleFunc("GET /api/v1/status-pages/{id}/posts", a.listStatusPosts)
	mux.HandleFunc("POST /api/v1/status-pages/{id}/posts", a.createStatusPost)
	mux.HandleFunc("POST /api/v1/status-pages/{id}/posts/{post}/updates", a.addStatusPostUpdate)
	mux.HandleFunc("DELETE /api/v1/status-pages/{id}/posts/{post}", a.deleteStatusPost)
	mux.HandleFunc("GET /status/{slug}", a.statusOnAppOrigin)
	mux.HandleFunc("GET /status/{slug}/{rest}", a.statusOnAppOrigin)
	mux.HandleFunc("GET /internal/domains/ask", a.domainAsk)
	mux.HandleFunc("GET /api/dashboard/v1/alert-channels", a.listChannels)
	mux.HandleFunc("POST /api/dashboard/v1/alert-channels", a.createChannel)
	mux.HandleFunc("PATCH /api/dashboard/v1/alert-channels/{id}", a.updateChannel)
	mux.HandleFunc("DELETE /api/dashboard/v1/alert-channels/{id}", a.deleteChannel)
	mux.HandleFunc("POST /api/dashboard/v1/alert-channels/{id}/test", a.testChannel)
	mux.HandleFunc("GET /api/dashboard/v1/alert-channels/{id}/deliveries", a.channelDeliveries)
	mux.HandleFunc("GET /api/dashboard/v1/incidents", a.listIncidents)
	mux.HandleFunc("POST /api/dashboard/v1/incidents/{id}/acknowledge", a.acknowledgeIncident)
	mux.HandleFunc("GET /api/dashboard/v1/maintenance", a.listMaintenance)
	mux.HandleFunc("POST /api/dashboard/v1/maintenance", a.createMaintenance)
	mux.HandleFunc("DELETE /api/dashboard/v1/maintenance/{id}", a.deleteMaintenance)
	mux.HandleFunc("GET /api/v1/alert-channels", a.listChannels)
	mux.HandleFunc("POST /api/v1/alert-channels", a.createChannel)
	mux.HandleFunc("PATCH /api/v1/alert-channels/{id}", a.updateChannel)
	mux.HandleFunc("DELETE /api/v1/alert-channels/{id}", a.deleteChannel)
	mux.HandleFunc("POST /api/v1/alert-channels/{id}/test", a.testChannel)
	mux.HandleFunc("GET /api/v1/alert-channels/{id}/deliveries", a.channelDeliveries)
	mux.HandleFunc("GET /api/v1/incidents", a.listIncidents)
	mux.HandleFunc("POST /api/v1/incidents/{id}/acknowledge", a.acknowledgeIncident)
	mux.HandleFunc("GET /api/v1/maintenance", a.listMaintenance)
	mux.HandleFunc("POST /api/v1/maintenance", a.createMaintenance)
	mux.HandleFunc("DELETE /api/v1/maintenance/{id}", a.deleteMaintenance)
	mux.HandleFunc("GET /ping/{token}", a.ping)
	mux.HandleFunc("POST /ping/{token}", a.ping)
	mux.HandleFunc("GET /api/dashboard/v1/monitors", a.listMonitors)
	mux.HandleFunc("POST /api/dashboard/v1/monitors", a.createMonitor)
	mux.HandleFunc("GET /api/dashboard/v1/monitors/{id}", a.getMonitor)
	mux.HandleFunc("PATCH /api/dashboard/v1/monitors/{id}", a.updateMonitor)
	mux.HandleFunc("DELETE /api/dashboard/v1/monitors/{id}", a.deleteMonitor)
	mux.HandleFunc("GET /api/dashboard/v1/monitors/{id}/results", a.monitorResults)
	mux.HandleFunc("GET /api/dashboard/v1/monitors/{id}/series", a.monitorSeries)
	mux.HandleFunc("GET /api/dashboard/v1/monitors/{id}/daily", a.monitorDaily)
	mux.HandleFunc("GET /api/dashboard/v1/regions", a.listRegions)
	mux.HandleFunc("GET /api/v1/monitors", a.listMonitors)
	mux.HandleFunc("POST /api/v1/monitors", a.createMonitor)
	mux.HandleFunc("GET /api/v1/monitors/{id}", a.getMonitor)
	mux.HandleFunc("PATCH /api/v1/monitors/{id}", a.updateMonitor)
	mux.HandleFunc("DELETE /api/v1/monitors/{id}", a.deleteMonitor)
	mux.HandleFunc("GET /api/v1/monitors/{id}/results", a.monitorResults)
	mux.HandleFunc("GET /api/v1/monitors/{id}/series", a.monitorSeries)
	mux.HandleFunc("GET /api/v1/monitors/{id}/daily", a.monitorDaily)
	mux.HandleFunc("GET /api/v1/regions", a.listRegions)
	mux.HandleFunc("POST /api/dashboard/v1/account/close", a.closeAccount)
	mux.HandleFunc("GET /api/dashboard/v1/members", a.listMembers)
	mux.HandleFunc("POST /api/dashboard/v1/members/invites", a.inviteMember)
	mux.HandleFunc("DELETE /api/dashboard/v1/members/invites/{id}", a.revokeInvite)
	mux.HandleFunc("POST /api/dashboard/v1/members/invites/{id}/resend", a.resendInvite)
	mux.HandleFunc("POST /api/dashboard/v1/members/invites/{token}/accept", a.acceptInvite)
	mux.HandleFunc("PATCH /api/dashboard/v1/members/{id}", a.updateMemberRole)
	mux.HandleFunc("DELETE /api/dashboard/v1/members/{id}", a.removeMember)
	mux.HandleFunc("GET /api/dashboard/v1/accounts", a.listAccounts)
	mux.HandleFunc("POST /api/dashboard/v1/accounts/{id}/switch", a.switchAccount)
	mux.HandleFunc("GET /api/dashboard/v1/api-keys", a.dashboardAPIKeys)
	mux.HandleFunc("POST /api/dashboard/v1/api-keys", a.createAPIKey)
	mux.HandleFunc("DELETE /api/dashboard/v1/api-keys/{id}", a.revokeAPIKey)
	mux.HandleFunc("GET /api/dashboard/v1/billing", a.dashboardBilling)
	mux.HandleFunc("POST /api/dashboard/v1/billing/checkout", a.billingCheckout)
	mux.HandleFunc("POST /api/dashboard/v1/billing/verify", a.billingVerify)
	mux.HandleFunc("POST /api/dashboard/v1/billing/renew", a.billingRenew)
	mux.HandleFunc("POST /api/dashboard/v1/billing/resume", a.billingResume)
	mux.HandleFunc("POST /api/dashboard/v1/billing/change-plan", a.billingChangePlan)
	mux.HandleFunc("POST /api/dashboard/v1/billing/cancel", a.billingCancel)
	mux.HandleFunc("POST /api/dashboard/v1/billing/manage-payment", a.managePayment)
	mux.HandleFunc("GET /api/v1/usage", a.usage)
	mux.HandleFunc("GET /api/v1/plans", a.publicPlans)
	mux.HandleFunc("POST /api/v1/auth/otp/request", a.requestOTP)
	mux.HandleFunc("POST /api/v1/auth/otp/verify", a.verifyOTP)
	mux.HandleFunc("GET /api/v1/me", a.dashboardMe)
	mux.HandleFunc("PATCH /api/v1/me", a.updateProfile)
	mux.HandleFunc("POST /api/v1/me/email", a.requestEmailChange)
	mux.HandleFunc("POST /api/v1/me/email/verify", a.verifyEmailChange)
	mux.HandleFunc("PATCH /api/v1/account", a.updateWorkspace)
	mux.HandleFunc("POST /api/v1/account/close", a.closeAccount)
	mux.HandleFunc("GET /api/v1/accounts", a.listAccounts)
	mux.HandleFunc("GET /api/v1/members", a.listMembers)
	mux.HandleFunc("POST /api/v1/members/invites", a.inviteMember)
	mux.HandleFunc("DELETE /api/v1/members/invites/{id}", a.revokeInvite)
	mux.HandleFunc("POST /api/v1/members/invites/{id}/resend", a.resendInvite)
	mux.HandleFunc("POST /api/v1/members/invites/{token}/accept", a.acceptInvite)
	mux.HandleFunc("PATCH /api/v1/members/{id}", a.updateMemberRole)
	mux.HandleFunc("DELETE /api/v1/members/{id}", a.removeMember)
	mux.HandleFunc("GET /api/v1/api-keys", a.dashboardAPIKeys)
	mux.HandleFunc("POST /api/v1/api-keys", a.createAPIKey)
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", a.revokeAPIKey)
	mux.HandleFunc("GET /api/v1/billing", a.dashboardBilling)
	mux.HandleFunc("POST /api/v1/billing/checkout", a.billingCheckout)
	mux.HandleFunc("POST /api/v1/billing/verify", a.billingVerify)
	mux.HandleFunc("POST /api/v1/billing/renew", a.billingRenew)
	mux.HandleFunc("POST /api/v1/billing/resume", a.billingResume)
	mux.HandleFunc("POST /api/v1/billing/change-plan", a.billingChangePlan)
	mux.HandleFunc("POST /api/v1/billing/cancel", a.billingCancel)
	mux.HandleFunc("POST /api/v1/billing/manage-payment", a.managePayment)
	mux.HandleFunc("POST "+probe.ClaimPath, a.probeClaim)
	mux.HandleFunc("POST "+probe.ResultsPath, a.probeResults)
	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.ready)
	mux.HandleFunc("GET /docs", a.docs)
	mux.HandleFunc("GET /docs/openapi.yaml", a.docsSpec)
	mux.HandleFunc("GET /docs/control.openapi.yaml", a.docsControlSpec)
	mux.HandleFunc("GET /docs/config.json", a.docsConfig)
	mux.HandleFunc("GET /billing/paystack/callback", a.billingCallback)
	mux.HandleFunc("POST /webhooks/paystack", a.paystackWebhook)
	mux.HandleFunc("GET /api/control/v1/session", a.controlSession)
	mux.HandleFunc("GET /api/control/v1/plans", a.controlPlans)
	mux.HandleFunc("PATCH /api/control/v1/plans/{plan}", a.controlUpdatePlan)
	mux.HandleFunc("GET /api/control/v1/stats", a.controlStats)
	mux.HandleFunc("GET /api/control/v1/accounts", a.controlAccounts)
	mux.HandleFunc("GET /api/control/v1/accounts/{id}", a.controlAccount)
	mux.HandleFunc("PATCH /api/control/v1/accounts/{id}/status", a.controlAccountStatus)
	mux.HandleFunc("GET /api/control/v1/members", a.controlCollection("members"))
	mux.HandleFunc("DELETE /api/control/v1/accounts/{accountID}/members/{principalID}", a.controlRemoveMember)
	mux.HandleFunc("GET /api/control/v1/invites", a.controlCollection("invites"))
	mux.HandleFunc("GET /api/control/v1/lifecycle", a.controlCollection("lifecycle"))
	mux.HandleFunc("GET /api/control/v1/health", a.controlHealth)
	mux.HandleFunc("GET /api/control/v1/usage", a.controlUsage)
	mux.HandleFunc("GET /api/control/v1/credentials", a.controlCredentials)
	mux.HandleFunc("POST /api/control/v1/credentials/{id}/revoke", a.controlRevokeCredential)
	mux.HandleFunc("GET /api/control/v1/billing", a.controlCollection("billing"))
	mux.HandleFunc("GET /api/control/v1/billing/checkouts", a.controlCollection("checkouts"))
	mux.HandleFunc("GET /api/control/v1/billing/events", a.controlCollection("events"))
	mux.HandleFunc("POST /api/control/v1/billing/{accountID}/reconcile", a.controlReconcileBilling)
	mux.HandleFunc("POST /api/control/v1/billing/{accountID}/complimentary", a.controlGrantComplimentary)
	mux.HandleFunc("DELETE /api/control/v1/billing/{accountID}/complimentary", a.controlRevokeComplimentary)
	mux.HandleFunc("GET /api/control/v1/email-jobs", a.controlCollection("jobs"))
	mux.HandleFunc("POST /api/control/v1/email-jobs/{id}/retry", a.controlRetryJob)
	mux.HandleFunc("GET /api/control/v1/access-grants", a.controlCollection("grants"))
	mux.HandleFunc("POST /api/control/v1/access-grants", a.controlGrant)
	mux.HandleFunc("POST /api/control/v1/access-grants/{id}/revoke", a.controlRevokeGrant)
	mux.HandleFunc("GET /api/control/v1/audit-events", a.controlCollection("audit"))
	mux.HandleFunc("GET /api/control/v1/probes", a.controlProbes)
	mux.HandleFunc("POST /api/control/v1/probes", a.controlCreateProbe)
	mux.HandleFunc("POST /api/control/v1/probes/{id}/revoke", a.controlRevokeProbe)
	mux.HandleFunc("/", a.notFound)
	return mux
}
