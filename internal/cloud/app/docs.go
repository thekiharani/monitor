package app

import (
	"io"
	"net/http"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
	"github.com/thekiharani/monitor/internal/cloud/openapi"
)

func (a *App) docs(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin) {
		return
	}
	http.Redirect(w, r, a.cfg.DashboardOrigin+"/docs", http.StatusPermanentRedirect)
}

func (a *App) docsSpec(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin) {
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = io.WriteString(w, openapi.Spec)
}

func (a *App) docsControlSpec(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin) {
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = io.WriteString(w, openapi.ControlSpec)
}

func (a *App) docsConfig(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin, a.cfg.AppOrigin, a.cfg.DashboardOrigin) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"api_origin":    a.cfg.APIOrigin,
		"app_origin":    a.cfg.AppOrigin,
		"status_origin": a.cfg.StatusOrigin,
		"dashboard_url": a.cfg.DashboardOrigin + "/docs",
	})
}
