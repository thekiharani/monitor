package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
)

func TestIntegrationControlAudienceAndCapabilityBoundaries(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	a.control = control.New(db, a.billing, a.cfg)

	email := fmt.Sprintf("control-boundary-%d@example.test", time.Now().UnixNano())
	accountID := createTestAccount(t, db, email)
	customerToken := createTestAPIKey(t, db, accountID)
	var customerPrincipalID string
	if err := db.QueryRow(context.Background(), `SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`, accountID).Scan(&customerPrincipalID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM access_grants WHERE principal_id=$1`, customerPrincipalID)
		deleteTestAccount(t, db, email)
	}()
	customerSession := "monitor_session_integration_" + uuid.NewV7().String()
	customerSessionID := uuid.NewV7().String()
	if _, err := db.Exec(context.Background(), `INSERT INTO credentials(id,principal_id,account_id,kind,audience,secret_hash,expires_at) VALUES($1,$2,$3,'session','customer_session',$4,now()+interval '1 hour')`, customerSessionID, customerPrincipalID, accountID, config.HashSecret(customerSession)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	principalID := uuid.NewV7().String()
	credentialID := uuid.NewV7().String()
	controlToken := "nmon_ctl_integration_" + uuid.NewV7().String()
	if _, err := db.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,'service','control boundary test')`, principalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM credentials WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID)
	})
	if _, err := db.Exec(ctx, `INSERT INTO credentials(id,principal_id,kind,audience,name,key_prefix,secret_hash,assurance,expires_at) VALUES($1,$2,'api_key','control_api','integration',$3,$4,'workload',now()+interval '1 hour')`, credentialID, principalID, controlToken[:24], config.HashSecret(controlToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','integration test grant')`, principalID, access.AccountsRead); err != nil {
		t.Fatal(err)
	}

	read := httptest.NewRecorder()
	readReq := testRequest(http.MethodGet, "/api/control/v1/accounts?q="+email, nil, a.cfg.APIOrigin)
	readReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccounts(read, readReq)
	if read.Code != http.StatusOK {
		t.Fatalf("authorized read status=%d body=%s", read.Code, read.Body.String())
	}
	if !strings.Contains(read.Body.String(), `"total"`) {
		t.Fatalf("authorized read missing pagination total: %s", read.Body.String())
	}

	badOffset := httptest.NewRecorder()
	badOffsetReq := testRequest(http.MethodGet, "/api/control/v1/accounts?offset=-1", nil, a.cfg.APIOrigin)
	badOffsetReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccounts(badOffset, badOffsetReq)
	if badOffset.Code != http.StatusBadRequest {
		t.Fatalf("invalid offset status=%d body=%s", badOffset.Code, badOffset.Body.String())
	}

	crossAudience := httptest.NewRecorder()
	crossReq := testRequest(http.MethodGet, "/api/control/v1/accounts", nil, a.cfg.APIOrigin)
	crossReq.Header.Set("Authorization", "Bearer "+customerToken)
	a.controlAccounts(crossAudience, crossReq)
	if crossAudience.Code != http.StatusUnauthorized {
		t.Fatalf("customer credential status=%d body=%s", crossAudience.Code, crossAudience.Body.String())
	}

	ordinarySession := httptest.NewRecorder()
	ordinaryReq := testRequest(http.MethodGet, "/api/control/v1/session", nil, a.cfg.APIOrigin)
	ordinaryReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	a.controlSession(ordinarySession, ordinaryReq)
	if ordinarySession.Code != http.StatusForbidden {
		t.Fatalf("customer session without grants status=%d body=%s", ordinarySession.Code, ordinarySession.Body.String())
	}

	if _, err := db.Exec(ctx, `INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','integration session grant')`, customerPrincipalID, access.AccountsRead); err != nil {
		t.Fatal(err)
	}
	operatorSession := httptest.NewRecorder()
	operatorReq := testRequest(http.MethodGet, "/api/control/v1/session", nil, a.cfg.APIOrigin)
	operatorReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	a.controlSession(operatorSession, operatorReq)
	if operatorSession.Code != http.StatusOK || !strings.Contains(operatorSession.Body.String(), access.AccountsRead) {
		t.Fatalf("operator session status=%d body=%s", operatorSession.Code, operatorSession.Body.String())
	}

	appHostSession := httptest.NewRecorder()
	appHostReq := testRequest(http.MethodGet, "/api/control/v1/session", nil, a.cfg.AppOrigin)
	appHostReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	a.controlSession(appHostSession, appHostReq)
	if appHostSession.Code != http.StatusOK {
		t.Fatalf("app host operator session status=%d body=%s", appHostSession.Code, appHostSession.Body.String())
	}

	missingCSRF := httptest.NewRecorder()
	missingCSRFReq := testRequestWithPathValue(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status", bytes.NewBufferString(`{"status":"suspended","reason":"integration csrf check"}`), a.cfg.APIOrigin, "id", accountID)
	missingCSRFReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	a.controlAccountStatus(missingCSRF, missingCSRFReq)
	if missingCSRF.Code != http.StatusUnauthorized {
		t.Fatalf("missing csrf status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}

	withCSRF := httptest.NewRecorder()
	withCSRFReq := testRequestWithPathValue(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status", bytes.NewBufferString(`{"status":"suspended","reason":"integration csrf check"}`), a.cfg.APIOrigin, "id", accountID)
	withCSRFReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	withCSRFReq.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "integration-csrf"})
	withCSRFReq.Header.Set("X-CSRF-Token", "integration-csrf")
	a.controlAccountStatus(withCSRF, withCSRFReq)
	if withCSRF.Code != http.StatusForbidden {
		t.Fatalf("valid csrf status=%d body=%s", withCSRF.Code, withCSRF.Body.String())
	}

	if _, err := db.Exec(ctx, `UPDATE credentials SET created_at=now()-interval '13 hours' WHERE id=$1`, customerSessionID); err != nil {
		t.Fatal(err)
	}
	staleSession := httptest.NewRecorder()
	staleReq := testRequest(http.MethodGet, "/api/control/v1/session", nil, a.cfg.APIOrigin)
	staleReq.AddCookie(&http.Cookie{Name: "__Host-session", Value: customerSession})
	a.controlSession(staleSession, staleReq)
	if staleSession.Code != http.StatusUnauthorized {
		t.Fatalf("stale operator session status=%d body=%s", staleSession.Code, staleSession.Body.String())
	}

	forbidden := httptest.NewRecorder()
	forbiddenReq := testRequestWithPathValue(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status", bytes.NewBufferString(`{"status":"suspended","reason":"integration authorization test"}`), a.cfg.APIOrigin, "id", accountID)
	forbiddenReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccountStatus(forbidden, forbiddenReq)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("missing capability status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestIntegrationControlAccountDetailStatsAndLifecycle(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	a.control = control.New(db, a.billing, a.cfg)

	email := fmt.Sprintf("control-workspace-%d@example.test", time.Now().UnixNano())
	accountID := createTestAccount(t, db, email)
	defer deleteTestAccount(t, db, email)

	ctx := context.Background()
	principalID := uuid.NewV7().String()
	credentialID := uuid.NewV7().String()
	controlToken := "nmon_ctl_workspace_" + uuid.NewV7().String()
	if _, err := db.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,'service','control workspace test')`, principalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM credentials WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID)
	})
	if _, err := db.Exec(ctx, `INSERT INTO credentials(id,principal_id,kind,audience,name,key_prefix,secret_hash,assurance,expires_at) VALUES($1,$2,'api_key','control_api','workspace',$3,$4,'workload',now()+interval '1 hour')`, credentialID, principalID, controlToken[:24], config.HashSecret(controlToken)); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{access.AccountsRead, access.AccountsSuspend, access.AccountsClose, access.AuditRead} {
		if _, err := db.Exec(ctx, `INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','workspace test')`, principalID, capability); err != nil {
			t.Fatal(err)
		}
	}

	secondOwnerID := uuid.NewV7().String()
	secondEmail := fmt.Sprintf("control-second-owner-%d@example.test", time.Now().UnixNano())
	if _, err := db.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,'human',$2)`, secondOwnerID, secondEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO principal_identities(principal_id,provider,provider_subject,verified_email) VALUES($1,'email_otp',$2,$2)`, secondOwnerID, secondEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,'owner')`, secondOwnerID, accountID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM principal_identities WHERE principal_id=$1`, secondOwnerID)
		_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, secondOwnerID)
	})

	detail := httptest.NewRecorder()
	detailReq := testRequestWithPathValue(http.MethodGet, "/api/control/v1/accounts/"+accountID, nil, a.cfg.APIOrigin, "id", accountID)
	detailReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccount(detail, detailReq)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), email) {
		t.Fatalf("account detail status=%d body=%s", detail.Code, detail.Body.String())
	}

	list := httptest.NewRecorder()
	listReq := testRequest(http.MethodGet, "/api/control/v1/accounts?q="+accountID, nil, a.cfg.APIOrigin)
	listReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccounts(list, listReq)
	var listBody struct {
		Data       []map[string]any `json:"data"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode account list: %v body=%s", err, list.Body.String())
	}
	if list.Code != http.StatusOK || len(listBody.Data) != 1 || listBody.Pagination.Total != 1 {
		t.Fatalf("account list should contain one tenant: status=%d rows=%d total=%d body=%s", list.Code, len(listBody.Data), listBody.Pagination.Total, list.Body.String())
	}

	stats := httptest.NewRecorder()
	statsReq := testRequest(http.MethodGet, "/api/control/v1/stats", nil, a.cfg.APIOrigin)
	statsReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlStats(stats, statsReq)
	if stats.Code != http.StatusOK || !strings.Contains(stats.Body.String(), `"accounts"`) {
		t.Fatalf("stats status=%d body=%s", stats.Code, stats.Body.String())
	}

	closeAccount := httptest.NewRecorder()
	closeReq := testRequestWithPathValue(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status", bytes.NewBufferString(`{"status":"closed","reason":"integration lifecycle closure"}`), a.cfg.APIOrigin, "id", accountID)
	closeReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccountStatus(closeAccount, closeReq)
	if closeAccount.Code != http.StatusNoContent {
		t.Fatalf("close account status=%d body=%s", closeAccount.Code, closeAccount.Body.String())
	}

	reopen := httptest.NewRecorder()
	reopenReq := testRequestWithPathValue(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status", bytes.NewBufferString(`{"status":"active","reason":"integration forbidden reopen"}`), a.cfg.APIOrigin, "id", accountID)
	reopenReq.Header.Set("Authorization", "Bearer "+controlToken)
	a.controlAccountStatus(reopen, reopenReq)
	if reopen.Code != http.StatusBadRequest {
		t.Fatalf("reopen closed account status=%d body=%s", reopen.Code, reopen.Body.String())
	}
}
