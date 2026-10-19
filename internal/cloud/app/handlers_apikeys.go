package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

func (a *App) dashboardMe(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	if isDashboardAPI(r) {
		if err := setCSRF(w, a.cfg.SessionTTL); err != nil {
			httpx.WriteError(w, r, eToHTTP("internal_error"))
			return
		}
	}
	a.writeProfile(w, r, p)
}

func (a *App) dashboardAPIKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	type item struct {
		ID, Name, Prefix                    string
		Created, LastUsed, Expires, Revoked *time.Time
	}
	items := make([]map[string]any, 0)
	if err := a.readTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(),
			`SELECT id::text,name,key_prefix,created_at,last_used_at,expires_at,revoked_at
			 FROM credentials
			 WHERE account_id=$1 AND kind='api_key' AND audience='customer_api'
			 ORDER BY created_at DESC`,
			p.AccountID.String(),
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x item
			if err = rows.Scan(&x.ID, &x.Name, &x.Prefix, &x.Created, &x.LastUsed, &x.Expires, &x.Revoked); err != nil {
				return err
			}
			status := "active"
			if x.Revoked != nil {
				status = "revoked"
			} else if x.Expires != nil && time.Now().After(*x.Expires) {
				status = "expired"
			}
			items = append(items, map[string]any{
				"id":           x.ID,
				"name":         x.Name,
				"prefix":       x.Prefix,
				"created_at":   httpx.RFC3339(x.Created),
				"last_used_at": httpx.RFC3339(x.LastUsed),
				"expires_at":   httpx.RFC3339(x.Expires),
				"revoked_at":   httpx.RFC3339(x.Revoked),
				"status":       status,
			})
		}
		return rows.Err()
	}); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *App) createAPIKey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	var in keyRequest
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") ||
		strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		name, formErr := boundedFormValue(w, r, "name")
		if formErr != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		in.Name = name
	} else {
		body, e := readBody(w, r, 64<<10)
		if e != nil || decode(body, &in) != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
	}
	name := strings.TrimSpace(in.Name)
	if len(name) == 0 || len(name) > 64 {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}

	account, plan := p.AccountID.String(), p.Plan
	if target := strings.TrimSpace(in.AccountID); target != "" && target != account {
		if _, parseErr := uuid.Parse(target); parseErr != nil {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		targetPlan, _, lookupErr := a.workspaceForKey(r.Context(), p.PrincipalID.String(), target)
		if errors.Is(lookupErr, errNoSuchWorkspace) {
			httpx.WriteError(w, r, eToHTTP("not_found"))
			return
		}
		if lookupErr != nil {
			httpx.WriteError(w, r, eToHTTP("service_unavailable"))
			return
		}
		account, plan = target, targetPlan
	}

	key, err := a.issueAPIKey(r.Context(), name, p.PrincipalID.String(), account, plan)
	if errors.Is(err, errPlanLimit) {
		httpx.WriteError(w, r, eToHTTP("plan_limit_exceeded"))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]string{
			"id":         key.id,
			"name":       key.name,
			"key":        key.secret,
			"prefix":     key.prefix,
			"account_id": account,
		},
	})
}

func (a *App) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") ||
			strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
			var formErr error
			id, formErr = boundedFormValue(w, r, "id")
			if formErr != nil {
				httpx.WriteError(w, r, eToHTTP("invalid_request"))
				return
			}
		} else {
			var in struct {
				ID string `json:"id"`
			}
			body, e := readBody(w, r, 64<<10)
			if e != nil || decode(body, &in) != nil {
				httpx.WriteError(w, r, eToHTTP("invalid_request"))
				return
			}
			id = in.ID
		}
	}
	if _, e := uuid.Parse(id); e != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var affected int64
	if err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`UPDATE credentials SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND account_id=$2 AND audience='customer_api'`,
			id, p.AccountID.String(),
		)
		affected = tag.RowsAffected()
		return err
	}); err != nil {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	if affected == 0 {
		httpx.WriteError(w, r, eToHTTP("not_found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func keyPrefix(key, marker string) string {
	body := strings.TrimPrefix(key, marker)
	return marker + body[:min(4, len(body))]
}
