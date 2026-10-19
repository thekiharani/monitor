package app

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/domain"
	"github.com/thekiharani/monitor/internal/cloud/httpx"
)

func billingRedirect(origin, paymentState string) string {
	return strings.TrimRight(origin, "/") + "/app/billing?payment=" + paymentState
}

func (a *App) paidPlan(plan string, currencies ...string) (config.PlanPricing, bool) {
	if a.plans != nil {
		currency := "KES"
		if len(currencies) > 0 {
			currency = currencies[0]
		}
		return a.plans.Pricing(plan, currency)
	}
	return a.cfg.Pricing(plan, currencies...)
}

func (a *App) checkoutTerms(ctx context.Context, reference, plan, currency string, transactions ...pgx.Tx) (config.PlanPricing, bool) {
	pricing, valid := a.cfg.Pricing(plan, currency)
	if !valid {
		return pricing, false
	}
	var code string
	var query interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	} = a.db
	if len(transactions) > 0 {
		query = transactions[0]
	}
	if err := query.QueryRow(ctx, `SELECT COALESCE(NULLIF(plan_amount_minor,0),amount_minor),COALESCE(provider_plan_code,'') FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&pricing.Amount, &code); err != nil {
		return pricing, false
	}
	if code != "" {
		pricing.Code = code
	}
	return pricing, true
}

func (a *App) subscriptionTerms(ctx context.Context, code, plan, currency string) (config.PlanPricing, bool) {
	pricing, valid := a.cfg.Pricing(plan, currency)
	if !valid {
		return pricing, false
	}
	var providerCode string
	if err := a.db.QueryRow(ctx, `SELECT amount_minor,COALESCE(provider_plan_code,'') FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&pricing.Amount, &providerCode); err != nil {
		return pricing, false
	}
	if providerCode != "" {
		pricing.Code = providerCode
	}
	return pricing, true
}

func (a *App) billingCurrency(ctx context.Context, accountID, requested string) (string, error) {
	requested = strings.ToUpper(strings.TrimSpace(requested))
	if requested != "" && requested != "KES" && requested != "USD" {
		return "", errBillingInvalid
	}
	var current string
	err := a.tenantRow(ctx, accountID, `SELECT currency FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') AND billing_mode<>'comp' ORDER BY effective_at DESC,created_at DESC LIMIT 1`, []any{accountID}, &current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if current != "" {
		if requested != "" && requested != current {
			return "", errBillingInvalid
		}
		return current, nil
	}
	if requested == "" {
		requested = "KES"
	}
	return requested, nil
}

func planRank(plan string) int {
	switch plan {
	case "starter":
		return 1
	case "pro":
		return 2
	case "business":
		return 3
	default:
		return 0
	}
}

func proratedUpgradeCharge(oldAmount, newAmount int64, periodStart, periodEnd, now time.Time) (int64, int64, error) {
	if oldAmount <= 0 || newAmount <= oldAmount || !periodEnd.After(periodStart) {
		return 0, 0, fmt.Errorf("invalid upgrade period or pricing")
	}
	total := periodEnd.Unix() - periodStart.Unix()
	remaining := min(max(periodEnd.Unix()-now.Unix(), 0), total)
	numerator := new(big.Int).Mul(big.NewInt(oldAmount), big.NewInt(remaining))
	numerator.Add(numerator, big.NewInt(total/2))
	credit := min(new(big.Int).Quo(numerator, big.NewInt(total)).Int64(), oldAmount)
	return newAmount - credit, credit, nil
}

func nextPaystackMonthlyRenewal(start time.Time) time.Time {
	start = start.UTC()
	day := min(start.Day(), 28)
	return time.Date(start.Year(), start.Month()+1, day, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), time.UTC)
}

const (
	billingModeAuto   = domain.BillingModeAuto
	billingModeManual = domain.BillingModeManual
	billingModeComp   = domain.BillingModeComp
)

func manualSubscriptionCode(reference string) string {
	return "MANUAL-" + reference
}

func (a *App) complimentaryPlanActive(ctx context.Context, accountID string) (bool, error) {
	var active bool
	err := a.tenantRow(ctx, accountID, `SELECT EXISTS(SELECT 1 FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp' AND status='active')`, []any{accountID}, &active)
	return active, err
}

func (a *App) rejectComplimentaryPlan(w http.ResponseWriter, r *http.Request, accountID, operation string) bool {
	active, err := a.complimentaryPlanActive(r.Context(), accountID)
	if err != nil {
		a.logError("complimentary plan lookup failed", "billing_api", operation, err, "account_id", accountID)
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return true
	}
	if active {
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("billing_complimentary_plan"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "unavailable"), http.StatusSeeOther)
		}
		return true
	}
	return false
}

func expectedPlanCodeFor(billingMode, checkoutType, planCode string) string {
	if billingMode == billingModeManual || checkoutType == "upgrade" {
		return ""
	}
	return planCode
}

func (a *App) dashboardBilling(w http.ResponseWriter, r *http.Request) {
	p, ok := a.auth(w, r)
	if !ok {
		return
	}
	var providerCode, status, currency, scheduledPlan, billingMode string
	var amount int64
	var periodStart, periodEnd, graceUntil, scheduledAt *time.Time
	err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,status,amount_minor,currency,current_period_start,current_period_end,grace_until,COALESCE(scheduled_plan,''),scheduled_plan_effective_at,COALESCE(billing_mode,'auto') FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String()}, &providerCode, &status, &amount, &currency, &periodStart, &periodEnd, &graceUntil, &scheduledPlan, &scheduledAt, &billingMode)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"plan": p.Plan, "subscription": nil}})
		return
	}
	if err != nil {
		a.logError("dashboard billing query failed", "billing_api", "read_current_subscription", err, "account_id", p.AccountID.String())
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"plan": p.Plan, "subscription": map[string]any{"provider_subscription_code": providerCode, "status": status, "amount_minor": amount, "currency": currency, "current_period_start": httpx.RFC3339(periodStart), "current_period_end": httpx.RFC3339(periodEnd), "grace_until": httpx.RFC3339(graceUntil), "billing_mode": billingMode, "scheduled_plan": nullableString(scheduledPlan), "scheduled_plan_effective_at": httpx.RFC3339(scheduledAt)}}})
}

type pendingCheckoutOutcome int

const (
	pendingNone pendingCheckoutOutcome = iota
	pendingReuse
	pendingSuperseded
	pendingSettled
	pendingUnknown
)

type pendingCheckout struct {
	plan, checkoutType, status, billingMode, currency string
	reference, authorizationURL, accessCode           string
	amount, credit                                    int64
	renewalAt                                         *time.Time
}

func (a *App) readPendingCheckout(ctx context.Context, accountID string) (pendingCheckout, error) {
	var c pendingCheckout
	err := a.tenantRow(ctx, accountID, `SELECT plan,COALESCE(checkout_type,''),status,COALESCE(billing_mode,'auto'),currency,provider_reference,COALESCE(authorization_url,''),COALESCE(provider_access_code,''),amount_minor,COALESCE(credit_minor,0),renewal_at FROM billing_checkouts WHERE account_id=$1 AND status IN ('pending','initialized') ORDER BY created_at DESC LIMIT 1`, []any{accountID},
		&c.plan, &c.checkoutType, &c.status, &c.billingMode, &c.currency, &c.reference, &c.authorizationURL, &c.accessCode, &c.amount, &c.credit, &c.renewalAt)
	return c, err
}

func (a *App) resolvePendingCheckout(ctx context.Context, accountID, plan, billingMode, currency string) (pendingCheckoutOutcome, pendingCheckout, error) {
	existing, err := a.readPendingCheckout(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pendingNone, pendingCheckout{}, nil
	}
	if err != nil {
		return pendingNone, pendingCheckout{}, err
	}
	if existing.status == "initialized" && existing.plan == plan && existing.billingMode == billingMode && existing.currency == currency &&
		existing.authorizationURL != "" && existing.accessCode != "" {
		return pendingReuse, existing, nil
	}

	transaction, verifyErr := a.billing.Verify(ctx, existing.reference)
	if verifyErr != nil {
		a.metrics.paystackFailures.Add(1)
		a.logWarn("abandoned checkout could not be verified", "billing_api", "verify_abandoned_checkout", verifyErr, "account_id", accountID, "reference", existing.reference, "existing_plan", existing.plan, "requested_plan", plan)
		return pendingUnknown, existing, nil
	}
	if transaction.Status == "success" {
		if _, err := a.verifyBillingCheckout(ctx, existing.reference, accountID); err != nil {
			a.logError("paid abandoned checkout could not be settled", "billing_api", "settle_abandoned_checkout", err, "account_id", accountID, "reference", existing.reference)
		}
		return pendingSettled, existing, nil
	}

	affected, err := a.tenantExec(ctx, accountID, `UPDATE billing_checkouts SET status='expired',updated_at=now() WHERE provider_reference=$1 AND account_id=$2 AND status IN ('pending','initialized')`, existing.reference, accountID)
	if err != nil {
		return pendingNone, existing, err
	}
	a.logEvent("abandoned checkout superseded", "billing_api", "supersede_abandoned_checkout", "account_id", accountID, "reference", existing.reference, "existing_plan", existing.plan, "existing_billing_mode", existing.billingMode, "requested_plan", plan, "requested_billing_mode", billingMode, "provider_status", transaction.Status, "rows", affected)
	return pendingSuperseded, existing, nil
}

func (a *App) billingCheckout(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	plan := strings.TrimSpace(r.URL.Query().Get("plan"))
	if plan == "" {
		plan = "pro"
	}
	method := strings.TrimSpace(r.URL.Query().Get("method"))
	billingMode := billingModeAuto
	if method == "mpesa" {
		billingMode = billingModeManual
	}
	if a.rejectComplimentaryPlan(w, r, p.AccountID.String(), "read_complimentary_plan_for_checkout") {
		return
	}
	currency, currencyErr := a.billingCurrency(r.Context(), p.AccountID.String(), r.URL.Query().Get("currency"))
	if currencyErr != nil || (method == "mpesa" && currency != "KES") {
		if currencyErr != nil && !errors.Is(currencyErr, errBillingInvalid) {
			writeBillingUnavailable(w, r, isJSONAPI(r))
		} else {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		}
		return
	}
	pricing, validPlan := a.paidPlan(plan, currency)
	if validPlan && billingMode == billingModeAuto && pricing.Code == "" {
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	if p.Status != "active" || !validPlan || pricing.Amount <= 0 || (p.Plan != "free" && planRank(plan) <= planRank(p.Plan)) {
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		}
		return
	}
	if p.Plan != "free" {
		var currentAmount int64
		var currentCurrency string
		var currentStart, currentEnd *time.Time
		recoveryErr := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT amount_minor,currency,current_period_start,current_period_end FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String(), p.Plan}, &currentAmount, &currentCurrency, &currentStart, &currentEnd)
		if errors.Is(recoveryErr, pgx.ErrNoRows) || (recoveryErr == nil && (currentAmount <= 0 || currentCurrency != currency || currentStart == nil || currentEnd == nil)) {
			if recoveryErr = a.recoverSubscription(r.Context(), p.AccountID.String()); recoveryErr != nil {
				a.logError("billing checkout subscription recovery failed", "billing_api", "recover_current_subscription", recoveryErr, "account_id", p.AccountID.String(), "requested_plan", plan)
				writeBillingUnavailable(w, r, isJSONAPI(r))
				return
			}
		} else if recoveryErr != nil {
			a.logError("billing checkout subscription lookup failed", "billing_api", "read_current_subscription", recoveryErr, "account_id", p.AccountID.String(), "requested_plan", plan)
			writeBillingUnavailable(w, r, isJSONAPI(r))
			return
		}
	}
	outcome, existing, resolveErr := a.resolvePendingCheckout(r.Context(), p.AccountID.String(), plan, billingMode, currency)
	if resolveErr != nil {
		a.logError("billing checkout pending-checkout resolution failed", "billing_api", "resolve_pending_checkout", resolveErr, "account_id", p.AccountID.String(), "requested_plan", plan)
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	switch outcome {
	case pendingReuse:
		if isJSONAPI(r) {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"authorization_url": existing.authorizationURL, "access_code": existing.accessCode, "reference": existing.reference, "currency": currency, "amount_minor": existing.amount, "credit_minor": existing.credit, "renewal_at": httpx.RFC3339(existing.renewalAt)}})
			return
		}
		http.Redirect(w, r, existing.authorizationURL, http.StatusSeeOther)
		return
	case pendingSettled:
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("billing_checkout_completed"))
			return
		}
		http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "success"), http.StatusSeeOther)
		return
	case pendingUnknown:
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("billing_checkout_pending"))
			return
		}
		http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		return
	}

	checkoutType := "subscription"
	amount, planAmount, credit := pricing.Amount, pricing.Amount, int64(0)
	var sourceSubscription string
	var renewalAt *time.Time
	checkoutID := uuid.NewV7()
	reference := "bil-" + checkoutID.String()
	var emailValue string
	err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(r.Context(), `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, p.AccountID.String()); execErr != nil {
			a.logError("billing checkout account lock failed", "billing_api", "lock_checkout_account", execErr, "account_id", p.AccountID.String(), "requested_plan", plan)
			return execErr
		}
		if p.Plan == "free" {
			var exists bool
			if scanErr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due','non_renewing'))`, p.AccountID.String()).Scan(&exists); scanErr != nil {
				a.logError("billing checkout subscription existence check failed", "billing_api", "check_existing_subscription", scanErr, "account_id", p.AccountID.String())
				return scanErr
			}
			if exists {
				return errBillingInvalid
			}
		} else {
			var oldAmount int64
			var oldCurrency string
			var periodStart, periodEnd *time.Time
			if scanErr := tx.QueryRow(r.Context(), `SELECT provider_subscription_code,amount_minor,currency,current_period_start,current_period_end FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at DESC,created_at DESC LIMIT 1 FOR UPDATE`, p.AccountID.String(), p.Plan).Scan(&sourceSubscription, &oldAmount, &oldCurrency, &periodStart, &periodEnd); scanErr != nil || periodStart == nil || periodEnd == nil || oldCurrency != currency {
				if scanErr != nil {
					a.logError("billing upgrade source subscription lookup failed", "billing_api", "read_upgrade_source_subscription", scanErr, "account_id", p.AccountID.String(), "current_plan", p.Plan, "requested_plan", plan)
					return scanErr
				}
				a.logError("billing upgrade source subscription invalid", "billing_api", "validate_upgrade_source_subscription", fmt.Errorf("missing period or unsupported currency"), "account_id", p.AccountID.String(), "current_plan", p.Plan, "requested_plan", plan, "currency", oldCurrency, "period_start_present", periodStart != nil, "period_end_present", periodEnd != nil)
				return errBillingUnavailable
			}
			var checkoutAt time.Time
			if scanErr := tx.QueryRow(r.Context(), `SELECT now()`).Scan(&checkoutAt); scanErr != nil {
				a.logError("billing upgrade clock lookup failed", "billing_api", "read_checkout_time", scanErr, "account_id", p.AccountID.String(), "requested_plan", plan)
				return scanErr
			}
			var chargeErr error
			amount, credit, chargeErr = proratedUpgradeCharge(oldAmount, pricing.Amount, *periodStart, *periodEnd, checkoutAt)
			if chargeErr != nil {
				a.logWarn("billing upgrade charge calculation rejected", "billing_api", "calculate_prorated_upgrade_charge", chargeErr, "account_id", p.AccountID.String(), "current_plan", p.Plan, "requested_plan", plan, "old_amount_minor", oldAmount, "new_amount_minor", pricing.Amount)
				return errBillingInvalid
			}
			checkoutType = "upgrade"
			renewal := nextPaystackMonthlyRenewal(checkoutAt)
			renewalAt = &renewal
		}
		var concurrent bool
		if scanErr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND status IN ('pending','initialized'))`, p.AccountID.String()).Scan(&concurrent); scanErr != nil {
			a.logError("billing checkout pending-checkout check failed", "billing_api", "check_pending_checkout", scanErr, "account_id", p.AccountID.String(), "requested_plan", plan)
			return scanErr
		}
		if concurrent {
			a.logWarn("billing checkout rejected because another checkout started concurrently", "billing_api", "reject_duplicate_checkout", nil, "account_id", p.AccountID.String(), "requested_plan", plan)
			return errCheckoutPending
		}
		if scanErr := tx.QueryRow(r.Context(), `
			SELECT i.verified_email FROM account_memberships m
			JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
			WHERE m.account_id=$1 AND m.role='owner' AND m.status='active' AND m.deleted_at IS NULL AND i.verified_email IS NOT NULL
			ORDER BY m.created_at,i.created_at LIMIT 1`, p.AccountID.String()).Scan(&emailValue); scanErr != nil {
			a.logError("billing checkout owner email lookup failed", "billing_api", "read_owner_email", scanErr, "account_id", p.AccountID.String(), "requested_plan", plan)
			return scanErr
		}
		_, execErr := tx.Exec(r.Context(), `INSERT INTO billing_checkouts(id,account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,source_subscription_code,renewal_at,provider_plan_code) VALUES($1,$2,'paystack',$3,$4,$5,$6,'pending',$7,$8,$9,$10,$11,$12,$13)`, checkoutID.String(), p.AccountID.String(), reference, plan, amount, currency, checkoutType, billingMode, planAmount, credit, sourceSubscription, renewalAt, pricing.Code)
		if execErr != nil {
			a.logError("billing checkout record insert failed", "billing_api", "create_checkout_record", execErr, "account_id", p.AccountID.String(), "reference", reference, "requested_plan", plan, "checkout_type", checkoutType, "amount_minor", amount, "credit_minor", credit, "billing_mode", billingMode)
		}
		return execErr
	})
	switch {
	case errors.Is(err, errBillingInvalid):
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	case errors.Is(err, errCheckoutPending):
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("billing_checkout_pending"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		}
		return
	case err != nil:
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	callback := strings.TrimRight(a.cfg.AppOrigin, "/") + "/billing/paystack/callback"
	providerPlanCode := pricing.Code
	var channels []string
	if currency == "USD" {
		channels = []string{"card"}
	}
	if billingMode == billingModeManual {
		providerPlanCode = ""
		channels = []string{"mobile_money"}
	} else if checkoutType == "upgrade" {
		providerPlanCode = ""
	}
	result, err := a.billing.InitializeCurrency(r.Context(), emailValue, reference, callback, p.AccountID.String(), checkoutID.String(), plan, providerPlanCode, currency, amount, channels)
	if err != nil {
		a.logError("billing provider checkout initialization failed", "billing_api", "initialize_provider_checkout", err, "account_id", p.AccountID.String(), "reference", reference, "requested_plan", plan, "checkout_type", checkoutType, "amount_minor", amount, "billing_mode", billingMode)
		if _, markErr := a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_checkouts SET status='failed',updated_at=now() WHERE id=$1`, checkoutID.String()); markErr != nil {
			a.logError("billing checkout failure state update failed", "billing_api", "mark_checkout_failed", markErr, "account_id", p.AccountID.String(), "reference", reference)
		}
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	_, err = a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_checkouts SET status='initialized',authorization_url=$1,provider_access_code=$2,updated_at=now() WHERE id=$3`, result.AuthorizationURL, result.AccessCode, checkoutID.String())
	if err != nil {
		a.logError("billing checkout provider response persistence failed", "billing_api", "persist_provider_checkout", err, "account_id", p.AccountID.String(), "reference", reference, "requested_plan", plan, "access_code_present", result.AccessCode != "", "authorization_url_present", result.AuthorizationURL != "")
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	if isJSONAPI(r) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"authorization_url": result.AuthorizationURL, "access_code": result.AccessCode, "reference": reference, "currency": currency, "amount_minor": amount, "credit_minor": credit, "renewal_at": httpx.RFC3339(renewalAt)}})
		return
	}
	http.Redirect(w, r, result.AuthorizationURL, http.StatusSeeOther)
}

func renewalChannels(currency string) []string {
	if currency == "USD" {
		return []string{"card"}
	}
	return []string{"mobile_money", "card"}
}

func (a *App) billingRenew(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	plan := strings.TrimSpace(r.URL.Query().Get("plan"))
	if plan == "" {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	if a.rejectComplimentaryPlan(w, r, p.AccountID.String(), "read_complimentary_plan_for_renewal") {
		return
	}
	currency, currencyErr := a.billingCurrency(r.Context(), p.AccountID.String(), r.URL.Query().Get("currency"))
	if currencyErr != nil {
		if errors.Is(currencyErr, errBillingInvalid) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			writeBillingUnavailable(w, r, true)
		}
		return
	}
	var renewalCode string
	if err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND billing_mode='manual' AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String(), plan}, &renewalCode); err != nil {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	pricing, validPlan := a.subscriptionTerms(r.Context(), renewalCode, plan, currency)
	if p.Status != "active" || !validPlan || pricing.Amount <= 0 || p.Plan != plan {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	outcome, existing, resolveErr := a.resolvePendingCheckout(r.Context(), p.AccountID.String(), plan, billingModeManual, currency)
	if resolveErr != nil {
		a.logError("billing renewal pending-checkout resolution failed", "billing_api", "resolve_pending_renewal_checkout", resolveErr, "account_id", p.AccountID.String(), "plan", plan)
		writeBillingUnavailable(w, r, true)
		return
	}
	switch outcome {
	case pendingReuse:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"authorization_url": existing.authorizationURL, "access_code": existing.accessCode, "reference": existing.reference, "currency": currency, "amount_minor": existing.amount}})
		return
	case pendingSettled:
		httpx.WriteError(w, r, eToHTTP("billing_checkout_completed"))
		return
	case pendingUnknown:
		httpx.WriteError(w, r, eToHTTP("billing_checkout_pending"))
		return
	}

	checkoutID := uuid.NewV7()
	reference := "bil-" + checkoutID.String()
	var emailValue string
	err := a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(r.Context(), `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, p.AccountID.String()); execErr != nil {
			a.logError("billing renewal account lock failed", "billing_api", "lock_renewal_account", execErr, "account_id", p.AccountID.String(), "plan", plan)
			return execErr
		}
		var status string
		var periodEnd *time.Time
		if scanErr := tx.QueryRow(r.Context(), `SELECT status,current_period_end FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND billing_mode='manual' AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at DESC,created_at DESC LIMIT 1 FOR UPDATE`, p.AccountID.String(), plan).Scan(&status, &periodEnd); scanErr != nil {
			if !errors.Is(scanErr, pgx.ErrNoRows) {
				a.logError("billing renewal subscription lookup failed", "billing_api", "read_subscription_for_renewal", scanErr, "account_id", p.AccountID.String(), "plan", plan)
				return scanErr
			}
			return errBillingInvalid
		}
		if status == "active" && periodEnd != nil && periodEnd.After(time.Now().Add(72*time.Hour)) {
			return errBillingInvalid
		}
		var concurrent bool
		if scanErr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM billing_checkouts WHERE account_id=$1 AND status IN ('pending','initialized'))`, p.AccountID.String()).Scan(&concurrent); scanErr != nil {
			a.logError("billing renewal pending-checkout check failed", "billing_api", "check_pending_renewal_checkout", scanErr, "account_id", p.AccountID.String(), "plan", plan)
			return scanErr
		}
		if concurrent {
			return errCheckoutPending
		}
		if scanErr := tx.QueryRow(r.Context(), `
			SELECT i.verified_email FROM account_memberships m
			JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
			WHERE m.account_id=$1 AND m.role='owner' AND m.status='active' AND m.deleted_at IS NULL AND i.verified_email IS NOT NULL
			ORDER BY m.created_at,i.created_at LIMIT 1`, p.AccountID.String()).Scan(&emailValue); scanErr != nil {
			a.logError("billing renewal owner email lookup failed", "billing_api", "read_owner_email_for_renewal", scanErr, "account_id", p.AccountID.String(), "plan", plan)
			return scanErr
		}
		_, execErr := tx.Exec(r.Context(), `INSERT INTO billing_checkouts(id,account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,provider_plan_code) VALUES($1,$2,'paystack',$3,$4,$5,$6,'pending','renewal','manual',$7,0,$8)`, checkoutID.String(), p.AccountID.String(), reference, plan, pricing.Amount, currency, pricing.Amount, pricing.Code)
		if execErr != nil {
			a.logError("billing renewal record insert failed", "billing_api", "create_renewal_checkout_record", execErr, "account_id", p.AccountID.String(), "reference", reference, "plan", plan)
		}
		return execErr
	})
	switch {
	case errors.Is(err, errBillingInvalid):
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	case errors.Is(err, errCheckoutPending):
		httpx.WriteError(w, r, eToHTTP("billing_checkout_pending"))
		return
	case err != nil:
		writeBillingUnavailable(w, r, true)
		return
	}
	callback := strings.TrimRight(a.cfg.AppOrigin, "/") + "/billing/paystack/callback"
	result, initErr := a.billing.InitializeCurrency(r.Context(), emailValue, reference, callback, p.AccountID.String(), checkoutID.String(), plan, "", currency, pricing.Amount, renewalChannels(currency))
	if initErr != nil {
		a.logError("billing renewal provider checkout initialization failed", "billing_api", "initialize_renewal_checkout", initErr, "account_id", p.AccountID.String(), "reference", reference, "plan", plan)
		if _, markErr := a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_checkouts SET status='failed',updated_at=now() WHERE id=$1`, checkoutID.String()); markErr != nil {
			a.logError("billing renewal failure state update failed", "billing_api", "mark_renewal_checkout_failed", markErr, "account_id", p.AccountID.String(), "reference", reference)
		}
		writeBillingUnavailable(w, r, true)
		return
	}
	if _, err = a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_checkouts SET status='initialized',authorization_url=$1,provider_access_code=$2,updated_at=now() WHERE id=$3`, result.AuthorizationURL, result.AccessCode, checkoutID.String()); err != nil {
		a.logError("billing renewal provider response persistence failed", "billing_api", "persist_renewal_checkout", err, "account_id", p.AccountID.String(), "reference", reference, "plan", plan)
		writeBillingUnavailable(w, r, true)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"authorization_url": result.AuthorizationURL, "access_code": result.AccessCode, "reference": reference, "currency": currency, "amount_minor": pricing.Amount}})
}

func (a *App) billingVerify(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("reference"))
	if reference == "" {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	checkoutType, err := a.verifyBillingCheckout(r.Context(), reference, p.AccountID.String())
	if err != nil {
		a.logError("billing verification failed", "billing_api", "verify_checkout", err, "account_id", p.AccountID.String(), "reference", reference)
		if errors.Is(err, errBillingVerificationFailed) || errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
			return
		}
		writeBillingUnavailable(w, r, true)
		return
	}
	if checkoutType == "subscription" {
		if err := a.recoverSubscription(r.Context(), p.AccountID.String()); err != nil {
			a.logError("billing inline verification subscription sync pending", "billing_api", "sync_verified_subscription", err, "account_id", p.AccountID.String(), "reference", reference)
		}
		a.retirePreviousSubscriptions(r.Context(), p.AccountID.String())
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "success"}})
}

var (
	errBillingVerificationFailed = errors.New("billing verification failed")
	errBillingActivationFailed   = errors.New("billing activation failed")
)

func (a *App) verifyBillingCheckout(ctx context.Context, reference, expectedAccountID string) (string, error) {
	var accountID, plan, currency, checkoutType, billingMode string
	var amount int64
	if err := a.db.QueryRow(ctx, `SELECT account_id::text,plan,amount_minor,currency,checkout_type,COALESCE(billing_mode,'auto') FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&accountID, &plan, &amount, &currency, &checkoutType, &billingMode); err != nil {
		return "", err
	}
	if expectedAccountID != "" && accountID != expectedAccountID {
		return "", errBillingVerificationFailed
	}
	transaction, err := a.billing.Verify(ctx, reference)
	if err != nil {
		return "", err
	}
	pricing, validPlan := a.checkoutTerms(ctx, reference, plan, currency)
	expectedPlanCode := expectedPlanCodeFor(billingMode, checkoutType, pricing.Code)
	if !validPlan || !paystack.ValidatePayment(transaction, accountID, amount, currency, expectedPlanCode, plan) {
		return "", errBillingVerificationFailed
	}
	if err := a.activateCheckout(ctx, reference, accountID, transaction.ID); err != nil {
		return "", fmt.Errorf("%w: %v", errBillingActivationFailed, err)
	}
	if checkoutType == "upgrade" {
		if err := a.completeUpgradeCheckout(ctx, reference, transaction.Customer.CustomerCode); err != nil {
			return "", fmt.Errorf("%w: %v", errBillingActivationFailed, err)
		}
	}
	return checkoutType, nil
}

func writeBillingUnavailable(w http.ResponseWriter, r *http.Request, dashboard bool) {
	if dashboard {
		httpx.WriteError(w, r, eToHTTP("service_unavailable"))
		return
	}
	http.Error(w, "unavailable", http.StatusServiceUnavailable)
}

func (a *App) cleanupBillingReplacement(ctx context.Context, code, token, accountID, operation string) {
	if err := a.billing.DisableSubscription(ctx, code, token); err != nil {
		a.logError("billing replacement subscription cleanup failed", "billing_api", "disable_failed_replacement_subscription", err, "account_id", accountID, "replacement_subscription_code", code, "operation", operation)
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *App) billingResume(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	billingCtx, cancelBilling := dbx.Cleanup(r.Context())
	defer cancelBilling()
	if recoverErr := a.recoverSubscription(r.Context(), p.AccountID.String()); recoverErr != nil {
		a.logError("billing resume subscription recovery failed", "billing_api", "recover_subscription_before_resume", recoverErr, "account_id", p.AccountID.String())
	}
	var code, customerCode, plan, status, scheduledPlan, scheduledCode, scheduledToken, billingMode, currency string
	var periodEnd *time.Time
	if err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,COALESCE(provider_customer_code,''),plan,status,current_period_end,COALESCE(scheduled_plan,''),COALESCE(billing_mode,'auto'),currency FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String()}, &code, &customerCode, &plan, &status, &periodEnd, &scheduledPlan, &billingMode, &currency); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			a.logError("billing resume subscription lookup failed", "billing_api", "read_subscription_for_resume", err, "account_id", p.AccountID.String())
			httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		}
		return
	}
	if billingMode == billingModeComp {
		httpx.WriteError(w, r, eToHTTP("billing_complimentary_plan"))
		return
	}
	if status != "non_renewing" {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	if scheduledPlan == plan {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "resume_scheduled", "plan": plan, "effective_at": httpx.RFC3339(periodEnd)}})
		return
	}
	if billingMode == billingModeManual {
		if _, err := a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_subscriptions SET status='active',cancel_at_period_end=false,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,renewal_reminded_at=NULL,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2 AND billing_mode='manual'`, code, p.AccountID.String()); err != nil {
			a.logError("billing resume manual subscription state update failed", "billing_api", "resume_manual_subscription", err, "account_id", p.AccountID.String(), "subscription_code", code)
			httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
			return
		}
		if scheduledPlan != "" {
			if _, err := a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_subscriptions SET status='canceled',cancel_at_period_end=true,updated_at=now() WHERE account_id=$1 AND plan=$2 AND billing_mode='manual' AND effective_at>now()`, p.AccountID.String(), scheduledPlan); err != nil {
				a.logError("billing resume manual scheduled subscription cleanup failed", "billing_api", "cancel_manual_scheduled_subscription", err, "account_id", p.AccountID.String(), "scheduled_plan", scheduledPlan)
				httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "active", "plan": plan}})
		return
	}
	if customerCode == "" || periodEnd == nil {
		a.logError("billing resume subscription is missing replacement inputs", "billing_api", "validate_replacement_subscription_inputs", fmt.Errorf("customer code or current period end is missing"), "account_id", p.AccountID.String(), "subscription_code", code, "plan", plan, "customer_code_present", customerCode != "", "period_end_present", periodEnd != nil)
		httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		return
	}
	pricing, validPlan := a.subscriptionTerms(r.Context(), code, plan, currency)
	if !validPlan || pricing.Code == "" {
		a.logError("billing resume plan is not configured", "billing_api", "validate_replacement_subscription_plan", fmt.Errorf("paid plan is not configured"), "account_id", p.AccountID.String(), "plan", plan)
		httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		return
	}
	startAt := periodEnd.UTC()
	if !startAt.After(time.Now().UTC()) {
		startAt = time.Now().UTC()
	}
	replacementEnd := nextPaystackMonthlyRenewal(startAt)
	created, err := a.billing.CreateSubscription(billingCtx, customerCode, pricing.Code, startAt)
	if err != nil {
		a.logError("billing resume replacement subscription creation failed", "billing_api", "create_replacement_subscription", err, "account_id", p.AccountID.String(), "cancelled_subscription_code", code, "plan", plan, "effective_at", startAt.Format(time.RFC3339))
		httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		return
	}
	encrypted, err := config.Seal(a.cfg.AppEncryptionKey, created.EmailToken)
	if err != nil {
		a.logError("billing resume replacement token encryption failed", "billing_api", "encrypt_replacement_subscription_token", err, "account_id", p.AccountID.String(), "replacement_subscription_code", created.SubscriptionCode, "plan", plan)
		if cleanupErr := a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken); cleanupErr != nil {
			a.logError("billing resume replacement cleanup failed", "billing_api", "disable_failed_replacement_subscription", cleanupErr, "account_id", p.AccountID.String(), "replacement_subscription_code", created.SubscriptionCode)
		}
		httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		return
	}
	if scheduledPlan != "" {
		if err = a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,provider_email_token_encrypted FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at>now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at LIMIT 1`, []any{p.AccountID.String(), scheduledPlan}, &scheduledCode, &scheduledToken); err != nil {
			a.logError("billing resume scheduled subscription lookup failed", "billing_api", "read_scheduled_subscription_for_resume", err, "account_id", p.AccountID.String(), "scheduled_plan", scheduledPlan)
			_ = a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken)
			httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
			return
		}
		scheduledPlain, decryptErr := config.Open(a.cfg.AppEncryptionKey, scheduledToken)
		if decryptErr != nil || scheduledPlain == "" {
			if decryptErr == nil {
				decryptErr = fmt.Errorf("scheduled subscription token is empty")
			}
			a.logError("billing resume scheduled subscription token unavailable", "billing_api", "decrypt_scheduled_subscription_token", decryptErr, "account_id", p.AccountID.String(), "scheduled_subscription_code", scheduledCode)
			_ = a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken)
			httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
			return
		}
		if err = a.billing.DisableSubscription(billingCtx, scheduledCode, scheduledPlain); err != nil {
			a.logError("billing resume scheduled subscription disable failed", "billing_api", "disable_scheduled_subscription", err, "account_id", p.AccountID.String(), "scheduled_subscription_code", scheduledCode)
			_ = a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken)
			httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
			return
		}
	}
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(r.Context(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,$4,$5,'active',$6,$7,$8,$9,$8,$10)`, p.AccountID.String(), customerCode, created.SubscriptionCode, encrypted, plan, pricing.Amount, currency, startAt, replacementEnd, pricing.Code); execErr != nil {
			return execErr
		}
		tag, updateErr := tx.Exec(r.Context(), `UPDATE billing_subscriptions SET scheduled_plan=$2,scheduled_plan_effective_at=$3,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$4 AND status='non_renewing'`, code, plan, startAt, p.AccountID.String())
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("cancelled subscription state was not updated")
		}
		return nil
	})
	if err != nil {
		a.logError("billing resume local state update failed", "billing_api", "persist_replacement_subscription", err, "account_id", p.AccountID.String(), "subscription_code", code, "replacement_subscription_code", created.SubscriptionCode, "scheduled_subscription_code", scheduledCode)
		if cleanupErr := a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken); cleanupErr != nil {
			a.logError("billing resume replacement cleanup failed", "billing_api", "disable_failed_replacement_subscription", cleanupErr, "account_id", p.AccountID.String(), "replacement_subscription_code", created.SubscriptionCode)
		}
		httpx.WriteError(w, r, eToHTTP("billing_resume_failed"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "resume_scheduled", "plan": plan, "effective_at": httpx.RFC3339(&startAt)}})
}

func (a *App) billingChangePlan(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	billingCtx, cancelBilling := dbx.Cleanup(r.Context())
	defer cancelBilling()
	targetPlan := strings.TrimSpace(r.URL.Query().Get("plan"))
	_, validPlan := a.paidPlan(targetPlan)
	if p.Status != "active" || p.Plan == "free" || !validPlan || planRank(targetPlan) >= planRank(p.Plan) {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	var code, customerCode, token, status, scheduledPlan, billingMode, currency string
	var periodEnd *time.Time
	if err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,COALESCE(provider_customer_code,''),COALESCE(provider_email_token_encrypted,''),status,current_period_end,COALESCE(scheduled_plan,''),COALESCE(billing_mode,'auto'),currency FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String()}, &code, &customerCode, &token, &status, &periodEnd, &scheduledPlan, &billingMode, &currency); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			a.logError("billing plan change subscription lookup failed", "billing_api", "read_subscription_for_plan_change", err, "account_id", p.AccountID.String(), "target_plan", targetPlan)
			writeBillingUnavailable(w, r, true)
		}
		return
	}
	pricing, validPlan := a.paidPlan(targetPlan, currency)
	if !validPlan || pricing.Amount <= 0 {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	if billingMode == billingModeComp {
		httpx.WriteError(w, r, eToHTTP("billing_complimentary_plan"))
		return
	}
	if scheduledPlan == targetPlan {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "scheduled", "plan": targetPlan, "effective_at": httpx.RFC3339(periodEnd)}})
		return
	}
	if billingMode == billingModeManual {
		if err := a.scheduleManualPlanChange(r.Context(), p.AccountID.String(), code, targetPlan, pricing.Amount, *periodEnd, scheduledPlan, currency, pricing.Code); err != nil {
			a.logError("billing plan change manual subscription scheduling failed", "billing_api", "schedule_manual_plan_change", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan)
			httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "scheduled", "plan": targetPlan, "effective_at": httpx.RFC3339(periodEnd)}})
		return
	}
	if periodEnd == nil || customerCode == "" {
		a.logError("billing plan change subscription is missing scheduling inputs", "billing_api", "validate_plan_change_inputs", fmt.Errorf("customer code or current period end is missing"), "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan, "customer_code_present", customerCode != "", "period_end_present", periodEnd != nil)
		httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
		return
	}
	var scheduledCode, scheduledToken, scheduledStatus, scheduledPlain string
	if scheduledPlan != "" {
		if err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,provider_email_token_encrypted,status FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at>now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at,created_at LIMIT 1`, []any{p.AccountID.String(), scheduledPlan}, &scheduledCode, &scheduledToken, &scheduledStatus); err != nil {
			a.logError("billing plan change scheduled subscription lookup failed", "billing_api", "read_scheduled_subscription_for_plan_change", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "scheduled_plan", scheduledPlan, "target_plan", targetPlan)
			httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
			return
		}
		if scheduledStatus != "non_renewing" {
			var decryptErr error
			scheduledPlain, decryptErr = config.Open(a.cfg.AppEncryptionKey, scheduledToken)
			if decryptErr != nil || scheduledPlain == "" {
				if decryptErr == nil {
					decryptErr = fmt.Errorf("scheduled subscription token is empty")
				}
				a.logError("billing plan change scheduled subscription token unavailable", "billing_api", "decrypt_scheduled_subscription_token_for_plan_change", decryptErr, "account_id", p.AccountID.String(), "scheduled_subscription_code", scheduledCode, "target_plan", targetPlan)
				httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
				return
			}
		}
	}
	created, err := a.billing.CreateSubscription(billingCtx, customerCode, pricing.Code, *periodEnd)
	if err != nil {
		a.logError("billing plan change provider subscription creation failed", "billing_api", "create_scheduled_subscription", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan, "effective_at", periodEnd.UTC().Format(time.RFC3339))
		httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
		return
	}
	encrypted, err := config.Seal(a.cfg.AppEncryptionKey, created.EmailToken)
	if err != nil {
		a.logError("billing plan change token encryption failed", "billing_api", "encrypt_new_subscription_token", err, "account_id", p.AccountID.String(), "new_subscription_code", created.SubscriptionCode, "target_plan", targetPlan)
		_ = a.billing.DisableSubscription(billingCtx, created.SubscriptionCode, created.EmailToken)
		httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
		return
	}
	plain := ""
	if status != "non_renewing" {
		plain, err = config.Open(a.cfg.AppEncryptionKey, token)
		if err != nil || plain == "" {
			if err != nil {
				a.logError("billing plan change current token decryption failed", "billing_api", "decrypt_current_subscription_token", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan)
			} else {
				a.logWarn("billing plan change current token empty", "billing_api", "validate_current_subscription_token", nil, "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan)
			}
			a.cleanupBillingReplacement(billingCtx, created.SubscriptionCode, created.EmailToken, p.AccountID.String(), "plan_change")
			httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
			return
		}
	}
	oldDisabled := false
	oldScheduledDisabled := false
	if scheduledCode != "" && scheduledStatus != "non_renewing" {
		if err = a.billing.DisableSubscription(billingCtx, scheduledCode, scheduledPlain); err != nil {
			a.logError("billing plan change scheduled subscription disable failed", "billing_api", "disable_previous_scheduled_subscription", err, "account_id", p.AccountID.String(), "scheduled_subscription_code", scheduledCode, "target_plan", targetPlan)
			a.cleanupBillingReplacement(billingCtx, created.SubscriptionCode, created.EmailToken, p.AccountID.String(), "plan_change")
			httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
			return
		}
		oldScheduledDisabled = true
	}
	if status != "non_renewing" {
		if err = a.billing.DisableSubscription(billingCtx, code, plain); err != nil {
			a.logError("billing plan change current subscription disable failed", "billing_api", "disable_current_subscription", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "target_plan", targetPlan)
			a.cleanupBillingReplacement(billingCtx, created.SubscriptionCode, created.EmailToken, p.AccountID.String(), "plan_change")
			httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
			return
		}
		oldDisabled = true
	}
	err = a.withTenant(r.Context(), p.AccountID.String(), func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(r.Context(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,$4,$5,'active',$6,$7,$8,$8,$9)`, p.AccountID.String(), customerCode, created.SubscriptionCode, encrypted, targetPlan, pricing.Amount, currency, *periodEnd, pricing.Code)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("scheduled subscription insert did not affect one row")
		}
		tag, execErr = tx.Exec(r.Context(), `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,scheduled_plan=$2,scheduled_plan_effective_at=$3,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$4`, code, targetPlan, *periodEnd, p.AccountID.String())
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("current subscription update did not affect one row")
		}
		if !oldScheduledDisabled {
			return nil
		}
		_, execErr = tx.Exec(r.Context(), `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, scheduledCode, p.AccountID.String())
		return execErr
	})
	if err != nil {
		a.logError("billing plan change local state update failed", "billing_api", "persist_plan_change", err, "account_id", p.AccountID.String(), "current_subscription_code", code, "new_subscription_code", created.SubscriptionCode, "target_plan", targetPlan)
		if oldDisabled {
			_ = a.billing.EnableSubscription(billingCtx, code, plain)
		}
		a.cleanupBillingReplacement(billingCtx, created.SubscriptionCode, created.EmailToken, p.AccountID.String(), "plan_change")
		httpx.WriteError(w, r, eToHTTP("billing_plan_change_failed"))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "scheduled", "plan": targetPlan, "effective_at": httpx.RFC3339(periodEnd)}})
}

func (a *App) scheduleManualPlanChange(ctx context.Context, accountID, code, targetPlan string, amount int64, periodEnd time.Time, scheduledPlan, currency, providerPlanCode string) error {
	if periodEnd.IsZero() {
		return fmt.Errorf("manual plan change missing period end")
	}
	return a.withTenant(ctx, accountID, func(tx pgx.Tx) error {
		if scheduledPlan != "" {
			if _, err := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',cancel_at_period_end=true,updated_at=now() WHERE account_id=$1 AND plan=$2 AND billing_mode='manual' AND effective_at>now()`, accountID, scheduledPlan); err != nil {
				return err
			}
		}
		newCode := manualSubscriptionCode(uuid.NewV7().String())
		periodStart := periodEnd.UTC()
		periodEndTime := nextPaystackMonthlyRenewal(periodStart)
		if _, err := tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,'active',$4,$5,$6,$7,'manual',$8,$9)`, accountID, newCode, targetPlan, amount, currency, periodStart, periodEndTime, periodStart, providerPlanCode); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,scheduled_plan=$2,scheduled_plan_effective_at=$3,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$4 AND billing_mode='manual'`, code, targetPlan, periodStart, accountID)
		return err
	})
}

func (a *App) retirePreviousSubscriptions(ctx context.Context, accountID string) {
	var currentPlan string
	if err := a.tenantRow(ctx, accountID, `SELECT plan FROM accounts WHERE id=$1`, []any{accountID}, &currentPlan); err != nil {
		a.logError("retiring previous subscriptions account lookup failed", "billing_worker", "read_current_account_plan", err, "account_id", accountID)
		return
	}
	type retiring struct{ code, encrypted, status string }
	previous := make([]retiring, 0, 4)
	if err := a.tenantRows(ctx, accountID, `SELECT provider_subscription_code,provider_email_token_encrypted,status FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND plan<>$2 AND billing_mode='auto' AND status IN ('active','past_due')`, []any{accountID, currentPlan}, func(rows pgx.Rows) error {
		var item retiring
		if err := rows.Scan(&item.code, &item.encrypted, &item.status); err != nil {
			return err
		}
		previous = append(previous, item)
		return nil
	}); err != nil {
		a.logError("retiring previous subscriptions query failed", "billing_worker", "list_previous_subscriptions", err, "account_id", accountID, "current_plan", currentPlan)
		return
	}
	for _, item := range previous {
		code, encrypted, status := item.code, item.encrypted, item.status
		token, decryptErr := config.Open(a.cfg.AppEncryptionKey, encrypted)
		if decryptErr != nil {
			a.logError("retiring previous billing subscription token decrypt failed", "billing_worker", "decrypt_previous_subscription_token", decryptErr, "account_id", accountID, "subscription_code", code)
			continue
		}
		if token == "" {
			a.logWarn("retiring previous billing subscription token empty", "billing_worker", "validate_previous_subscription_token", nil, "account_id", accountID, "subscription_code", code)
			continue
		}
		if disableErr := a.billing.DisableSubscription(ctx, code, token); disableErr != nil {
			a.logError("retiring previous billing subscription failed", "billing_worker", "disable_previous_subscription", disableErr, "account_id", accountID, "subscription_code", code, "previous_status", status)
			continue
		}
		if _, err := a.tenantExec(ctx, accountID, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, code, accountID); err != nil {
			a.logError("retiring previous billing subscription state update failed", "billing_worker", "persist_previous_subscription_state", err, "account_id", accountID, "subscription_code", code)
		}
	}
}

func (a *App) applyScheduledPlanChanges(ctx context.Context) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.logError("scheduled billing plan transaction begin failed", "billing_worker", "begin_scheduled_plan_transaction", err)
		return
	}
	defer dbx.Rollback(ctx, tx)
	rows, err := tx.Query(ctx, `SELECT account_id::text,scheduled_plan FROM billing_subscriptions WHERE scheduled_plan IS NOT NULL AND scheduled_plan_effective_at<=now() FOR UPDATE`)
	if err != nil {
		a.logError("scheduled billing plan query failed", "billing_worker", "read_scheduled_plan_changes", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, plan string
		if err := rows.Scan(&accountID, &plan); err != nil {
			a.logError("scheduled billing plan scan failed", "billing_worker", "scan_scheduled_plan_change", err)
			return
		}
		if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active' AND EXISTS(SELECT 1 FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at<=now() AND status IN ('active','past_due','non_renewing'))`, accountID, plan); err != nil {
			a.logError("scheduled billing account plan update failed", "billing_worker", "apply_scheduled_account_plan", err, "account_id", accountID, "plan", plan)
			return
		}
	}
	if err = rows.Err(); err != nil {
		a.logError("scheduled billing plan iteration failed", "billing_worker", "iterate_scheduled_plan_changes", err)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET scheduled_plan=NULL,scheduled_plan_effective_at=NULL,updated_at=now() WHERE scheduled_plan IS NOT NULL AND scheduled_plan_effective_at<=now()`); err != nil {
		a.logError("scheduled billing plan cleanup failed", "billing_worker", "clear_scheduled_plan_fields", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.logError("scheduled billing plan update failed", "billing_worker", "apply_scheduled_plan_changes", err)
	}
}

func (a *App) billingCallback(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.AppOrigin) {
		return
	}
	reference := r.URL.Query().Get("reference")
	if reference == "" {
		http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		return
	}
	var accountID string
	if err := a.db.QueryRow(r.Context(), `SELECT account_id::text FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&accountID); err != nil {
		http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		return
	}
	checkoutType, err := a.verifyBillingCheckout(r.Context(), reference, "")
	if err != nil {
		if errors.Is(err, errBillingActivationFailed) {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "failed"), http.StatusSeeOther)
		return
	}
	if checkoutType == "subscription" {
		if err := a.recoverSubscription(r.Context(), accountID); err != nil {
			a.logError("billing callback subscription sync pending", "billing_webhook", "sync_verified_subscription", err, "account_id", accountID, "reference", reference)
		}
		a.retirePreviousSubscriptions(r.Context(), accountID)
	}
	http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "success"), http.StatusSeeOther)
}

func (a *App) activateCheckout(ctx context.Context, reference, accountID string, providerTransactionID int64) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	var status, plan, checkoutType, billingMode string
	if err = tx.QueryRow(ctx, `SELECT status,plan,checkout_type,COALESCE(billing_mode,'auto') FROM billing_checkouts WHERE provider_reference=$1 FOR UPDATE`, reference).Scan(&status, &plan, &checkoutType, &billingMode); err != nil {
		return err
	}
	newlySuccessful := status != "successful"
	if newlySuccessful {
		txID := ""
		if providerTransactionID > 0 {
			txID = fmt.Sprintf("%d", providerTransactionID)
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_checkouts SET status='successful',provider_transaction_id=COALESCE(NULLIF($2,''),provider_transaction_id),completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE provider_reference=$1`, reference, txID); err != nil {
			return err
		}
	}
	if checkoutType == "subscription" {
		if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, accountID, plan); err != nil {
			return err
		}
	}
	if newlySuccessful && billingMode == billingModeManual && checkoutType != "upgrade" {
		if err := a.applyManualCheckoutActivation(ctx, tx, reference, accountID, plan, checkoutType); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *App) applyManualCheckoutActivation(ctx context.Context, tx pgx.Tx, reference, accountID, plan, checkoutType string) error {
	var amount int64
	var currency string
	if err := tx.QueryRow(ctx, `SELECT amount_minor,currency FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&amount, &currency); err != nil {
		return err
	}
	pricing, validPlan := a.checkoutTerms(ctx, reference, plan, currency, tx)
	if !validPlan || pricing.Amount <= 0 {
		return fmt.Errorf("manual checkout activation has unknown plan or currency account_id=%s plan=%s currency=%s", accountID, plan, currency)
	}
	if amount != pricing.Amount {
		return fmt.Errorf("manual checkout activation amount mismatch reference=%s amount_minor=%d expected=%d", reference, amount, pricing.Amount)
	}
	code := manualSubscriptionCode(reference)
	now := time.Now().UTC()
	switch checkoutType {
	case "subscription":
		periodStart := now
		periodEnd := nextPaystackMonthlyRenewal(now)
		tag, err := tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,'active',$4,$5,$6,$7,'manual',$8,$9)
			ON CONFLICT(provider_subscription_code) DO UPDATE SET status='active',grace_until=NULL,cancel_at_period_end=false,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,renewal_reminded_at=NULL,current_period_start=$6,current_period_end=$7,effective_at=$8,amount_minor=$4,currency=$5,updated_at=now() WHERE billing_subscriptions.account_id=EXCLUDED.account_id`,
			accountID, code, plan, pricing.Amount, currency, periodStart, periodEnd, now, pricing.Code)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("manual checkout activation account mismatch reference=%s", reference)
		}
	case "renewal":
		var currentEnd *time.Time
		_ = tx.QueryRow(ctx, `SELECT current_period_end FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND billing_mode='manual' ORDER BY effective_at DESC,created_at DESC LIMIT 1`, accountID, plan).Scan(&currentEnd)
		periodStart := now
		if currentEnd != nil && currentEnd.After(now) {
			periodStart = currentEnd.UTC()
		}
		periodEnd := nextPaystackMonthlyRenewal(periodStart)
		tag, err := tx.Exec(ctx, `UPDATE billing_subscriptions SET status='active',grace_until=NULL,cancel_at_period_end=false,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,renewal_reminded_at=NULL,current_period_start=$2,current_period_end=$3,effective_at=now(),updated_at=now() WHERE account_id=$1 AND plan=$4 AND billing_mode='manual' AND currency=$5 AND effective_at<=now() AND status IN ('active','past_due','non_renewing')`, accountID, periodStart, periodEnd, plan, currency)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("manual renewal found no subscription account_id=%s plan=%s reference=%s", accountID, plan, reference)
		}
	default:
		return fmt.Errorf("manual checkout activation unsupported checkout_type=%s reference=%s", checkoutType, reference)
	}
	return nil
}

type upgradeCheckout struct {
	accountID, targetPlan, currency, status string
	sourceCode, sourceCustomerCode          string
	sourceToken, sourceStatus               string
	sourceScheduledPlan, sourceMode         string
	checkoutMode                            string
	planAmount                              int64
	renewalAt                               time.Time
	completedAt, provisionedAt              *time.Time
}

func (a *App) readUpgradeCheckout(ctx context.Context, reference string) (upgradeCheckout, error) {
	var c upgradeCheckout
	err := a.db.QueryRow(ctx, `SELECT c.account_id::text,c.plan,c.currency,c.status,c.plan_amount_minor,c.source_subscription_code,c.renewal_at,c.completed_at,c.provisioned_at,COALESCE(s.provider_customer_code,''),COALESCE(s.provider_email_token_encrypted,''),s.status,COALESCE(s.scheduled_plan,''),COALESCE(s.billing_mode,'auto'),COALESCE(c.billing_mode,'auto') FROM billing_checkouts c JOIN billing_subscriptions s ON s.provider_subscription_code=c.source_subscription_code AND s.account_id=c.account_id WHERE c.provider_reference=$1 AND c.checkout_type='upgrade'`, reference).
		Scan(&c.accountID, &c.targetPlan, &c.currency, &c.status, &c.planAmount, &c.sourceCode, &c.renewalAt, &c.completedAt, &c.provisionedAt, &c.sourceCustomerCode, &c.sourceToken, &c.sourceStatus, &c.sourceScheduledPlan, &c.sourceMode, &c.checkoutMode)
	return c, err
}

func (a *App) completeUpgradeCheckout(ctx context.Context, reference, transactionCustomerCode string) error {
	if reference == "" {
		return nil
	}
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	lockKey := "monitor:upgrade:" + reference
	locked, err := acquireAdvisoryLock(ctx, conn, lockKey)
	if err != nil {
		return err
	}
	if !locked {
		a.logWarn("upgrade checkout provisioning already in flight", "billing_provisioning", "acquire_upgrade_lock", nil, "reference", reference, "skipped", true)
		return nil
	}
	defer func() {
		releaseCtx, cancel := dbx.Cleanup(ctx)
		defer cancel()
		if releaseErr := releaseAdvisoryLock(releaseCtx, conn, lockKey); releaseErr != nil {
			a.logError("upgrade checkout lock release failed", "billing_provisioning", "release_upgrade_lock", releaseErr, "reference", reference)
		}
	}()

	checkout, err := a.readUpgradeCheckout(ctx, reference)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if checkout.provisionedAt != nil {
		return nil
	}
	if checkout.checkoutMode == billingModeManual {
		return a.completeManualUpgradeCheckout(ctx, reference, checkout)
	}
	return a.completeAutoUpgradeCheckout(ctx, reference, transactionCustomerCode, checkout)
}

func (a *App) completeAutoUpgradeCheckout(ctx context.Context, reference, transactionCustomerCode string, checkout upgradeCheckout) error {
	pricing, validPlan := a.checkoutTerms(ctx, reference, checkout.targetPlan, checkout.currency)
	if checkout.status != "successful" || checkout.completedAt == nil || !validPlan || checkout.planAmount != pricing.Amount || checkout.sourceCustomerCode == "" {
		return fmt.Errorf("upgrade checkout invariants failed")
	}
	if transactionCustomerCode != "" && transactionCustomerCode != checkout.sourceCustomerCode {
		return fmt.Errorf("upgrade customer mismatch")
	}

	var scheduledCode, scheduledToken, scheduledStatus, scheduledPlain string
	if checkout.sourceScheduledPlan != "" {
		if err := a.db.QueryRow(ctx, `SELECT provider_subscription_code,provider_email_token_encrypted,status FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at>now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at,created_at LIMIT 1`, checkout.accountID, checkout.sourceScheduledPlan).Scan(&scheduledCode, &scheduledToken, &scheduledStatus); err != nil {
			return fmt.Errorf("pending scheduled subscription lookup: %w", err)
		}
		if scheduledStatus != "non_renewing" {
			plain, err := config.Open(a.cfg.AppEncryptionKey, scheduledToken)
			if err != nil || plain == "" {
				if err == nil {
					err = fmt.Errorf("pending scheduled subscription token is empty")
				}
				return err
			}
			scheduledPlain = plain
		}
	}
	sourcePlain, err := config.Open(a.cfg.AppEncryptionKey, checkout.sourceToken)
	if err != nil || sourcePlain == "" {
		return fmt.Errorf("upgrade source subscription token unavailable")
	}

	created, err := a.billing.CreateSubscription(ctx, checkout.sourceCustomerCode, pricing.Code, checkout.renewalAt)
	if err != nil {
		return err
	}
	undoCtx, cancelUndo := dbx.Cleanup(ctx)
	defer cancelUndo()
	encrypted, err := config.Seal(a.cfg.AppEncryptionKey, created.EmailToken)
	if err != nil {
		a.cleanupBillingReplacement(undoCtx, created.SubscriptionCode, created.EmailToken, checkout.accountID, "upgrade")
		return err
	}
	if scheduledCode != "" && scheduledStatus != "non_renewing" {
		if err = a.billing.DisableSubscription(ctx, scheduledCode, scheduledPlain); err != nil {
			a.cleanupBillingReplacement(undoCtx, created.SubscriptionCode, created.EmailToken, checkout.accountID, "upgrade")
			return fmt.Errorf("pending scheduled subscription disable: %w", err)
		}
	}
	oldDisabled := false
	if checkout.sourceStatus != "non_renewing" {
		if err = a.billing.DisableSubscription(ctx, checkout.sourceCode, sourcePlain); err != nil {
			a.cleanupBillingReplacement(undoCtx, created.SubscriptionCode, created.EmailToken, checkout.accountID, "upgrade")
			return err
		}
		oldDisabled = true
	}

	err = a.persistUpgradeProvisioning(ctx, reference, checkout, created.SubscriptionCode, encrypted, scheduledCode)
	if err != nil {
		if oldDisabled {
			if enableErr := a.billing.EnableSubscription(undoCtx, checkout.sourceCode, sourcePlain); enableErr != nil {
				a.logError("upgrade rollback could not restore the source subscription", "billing_provisioning", "enable_source_subscription", enableErr, "account_id", checkout.accountID, "subscription_code", checkout.sourceCode, "reference", reference)
			}
		}
		a.cleanupBillingReplacement(undoCtx, created.SubscriptionCode, created.EmailToken, checkout.accountID, "upgrade")
		return err
	}
	return nil
}

func (a *App) persistUpgradeProvisioning(ctx context.Context, reference string, checkout upgradeCheckout, newCode, encryptedToken, scheduledCode string) error {
	pricing, valid := a.checkoutTerms(ctx, reference, checkout.targetPlan, checkout.currency)
	if !valid {
		return errBillingVerificationFailed
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,$4,$5,'active',$6,$7,$8,$9,$8,$10) ON CONFLICT(provider_subscription_code) DO UPDATE SET provider_customer_code=EXCLUDED.provider_customer_code,provider_email_token_encrypted=EXCLUDED.provider_email_token_encrypted,plan=EXCLUDED.plan,status='active',amount_minor=EXCLUDED.amount_minor,currency=EXCLUDED.currency,current_period_start=EXCLUDED.current_period_start,current_period_end=EXCLUDED.current_period_end,effective_at=EXCLUDED.effective_at,updated_at=now() WHERE billing_subscriptions.account_id=EXCLUDED.account_id`, checkout.accountID, checkout.sourceCustomerCode, newCode, encryptedToken, checkout.targetPlan, checkout.planAmount, checkout.currency, *checkout.completedAt, checkout.renewalAt, pricing.Code); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, checkout.sourceCode, checkout.accountID); err != nil {
		return err
	}
	if scheduledCode != "" {
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, scheduledCode, checkout.accountID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, checkout.accountID, checkout.targetPlan); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE billing_checkouts SET upgrade_subscription_code=$2,provisioned_at=now(),updated_at=now() WHERE provider_reference=$1 AND provisioned_at IS NULL`, reference, newCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("upgrade checkout was already provisioned reference=%s", reference)
	}
	return tx.Commit(ctx)
}

func (a *App) completeManualUpgradeCheckout(ctx context.Context, reference string, checkout upgradeCheckout) error {
	pricing, validPlan := a.checkoutTerms(ctx, reference, checkout.targetPlan, checkout.currency)
	if checkout.status != "successful" || checkout.completedAt == nil || !validPlan || checkout.planAmount != pricing.Amount || checkout.renewalAt.IsZero() {
		return fmt.Errorf("manual upgrade checkout invariants failed")
	}
	renewalAt := checkout.renewalAt.UTC()
	now := time.Now().UTC()

	type retiring struct{ code, plain string }
	var toDisable []retiring
	targetCode := checkout.sourceCode
	if checkout.sourceMode != billingModeManual {
		if checkout.sourceToken != "" {
			plain, err := config.Open(a.cfg.AppEncryptionKey, checkout.sourceToken)
			if err != nil {
				return err
			}
			if plain == "" {
				return fmt.Errorf("manual upgrade source subscription token unavailable")
			}
			toDisable = append(toDisable, retiring{checkout.sourceCode, plain})
		}
		targetCode = manualSubscriptionCode(uuid.NewV7().String())
	}
	type scheduledSub struct{ code, token, mode string }
	var scheduledSubs []scheduledSub
	if checkout.sourceScheduledPlan != "" {
		rows, err := a.db.Query(ctx, `SELECT provider_subscription_code,COALESCE(provider_email_token_encrypted,''),COALESCE(billing_mode,'auto') FROM billing_subscriptions WHERE account_id=$1 AND plan=$2 AND effective_at>now() AND status IN ('active','past_due','non_renewing')`, checkout.accountID, checkout.sourceScheduledPlan)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item scheduledSub
			if err = rows.Scan(&item.code, &item.token, &item.mode); err != nil {
				rows.Close()
				return err
			}
			scheduledSubs = append(scheduledSubs, item)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, item := range scheduledSubs {
			if item.mode != billingModeManual && item.token != "" {
				if plain, decryptErr := config.Open(a.cfg.AppEncryptionKey, item.token); decryptErr == nil && plain != "" {
					toDisable = append(toDisable, retiring{item.code, plain})
				}
			}
		}
	}

	for _, item := range toDisable {
		if err := a.billing.DisableSubscription(ctx, item.code, item.plain); err != nil {
			return fmt.Errorf("disable superseded subscription %s: %w", item.code, err)
		}
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	if checkout.sourceMode != billingModeManual {
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, checkout.sourceCode, checkout.accountID); err != nil {
			return err
		}
	}
	for _, item := range scheduledSubs {
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1`, item.code); err != nil {
			return err
		}
	}
	if checkout.sourceMode == billingModeManual {
		tag, execErr := tx.Exec(ctx, `UPDATE billing_subscriptions SET plan=$2,amount_minor=$3,status='active',grace_until=NULL,cancel_at_period_end=false,scheduled_plan=NULL,scheduled_plan_effective_at=NULL,renewal_reminded_at=NULL,current_period_end=$4,effective_at=now(),updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$5 AND billing_mode='manual'`, checkout.sourceCode, checkout.targetPlan, checkout.planAmount, renewalAt, checkout.accountID)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("manual upgrade source subscription missing account_id=%s code=%s", checkout.accountID, checkout.sourceCode)
		}
	} else if _, err = tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,effective_at,provider_plan_code) VALUES($1,'paystack',$2,$3,'active',$4,$5,$6,$7,'manual',$8,$9)`, checkout.accountID, targetCode, checkout.targetPlan, checkout.planAmount, checkout.currency, checkout.completedAt.UTC(), renewalAt, now, pricing.Code); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, checkout.accountID, checkout.targetPlan); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE billing_checkouts SET upgrade_subscription_code=$2,provisioned_at=now(),updated_at=now() WHERE provider_reference=$1 AND provisioned_at IS NULL`, reference, targetCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("manual upgrade checkout was already provisioned reference=%s", reference)
	}
	return tx.Commit(ctx)
}

func (a *App) paystackWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.requireHost(w, r, a.cfg.APIOrigin) {
		return
	}
	body, err := readBody(w, r, 512<<10)
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !paystack.VerifySignature(a.cfg.PaystackSecretKey, body, []byte(r.Header.Get("x-paystack-signature"))) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	env, err := paystack.ParseWebhook(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	storedPayload, err := paystack.SanitizeWebhookPayload(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx := paystack.ExtractContext(env.Event, env.Data)
	ignored := !knownPaystackEvent(env.Event)
	if err = a.processBillingEvent(r.Context(), env.Event, storedPayload, sha256Bytes(body), ctx, ignored); err != nil {
		a.metrics.paystackFailures.Add(1)
		a.recordFailedBillingEvent(r.Context(), env.Event, storedPayload, sha256Bytes(body), err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if env.Event == "charge.success" {
		if err = a.completeUpgradeCheckout(r.Context(), ctx.Reference, ctx.CustomerCode); err != nil {
			a.metrics.paystackFailures.Add(1)
			a.logError("upgrade checkout provisioning pending", "billing_webhook", "provision_upgrade_checkout", err, "reference", ctx.Reference, "customer_code_present", ctx.CustomerCode != "")
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (a *App) recordFailedBillingEvent(ctx context.Context, event string, payload, hash []byte, processingErr error) {
	_, err := a.db.Exec(ctx, `INSERT INTO billing_events(provider,payload_hash,event_type,payload,status,error_message) VALUES('paystack',$1,$2,$3,'failed',$4) ON CONFLICT(payload_hash) DO UPDATE SET status='failed',error_message=EXCLUDED.error_message WHERE billing_events.status NOT IN ('processed','ignored')`, hash, event, payload, processingErr.Error())
	if err != nil {
		a.logError("billing event failure record failed", "billing_webhook", "record_failed_event", err, "event_type", event, "payload_bytes", len(payload))
	}
}

func knownPaystackEvent(event string) bool {
	switch event {
	case "charge.success", "subscription.create", "invoice.create", "invoice.update", "invoice.payment_failed", "subscription.not_renew", "subscription.disable", "subscription.expiring_cards":
		return true
	default:
		return false
	}
}

func sha256Bytes(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

func (a *App) processBillingEvent(ctx context.Context, event string, payload, hash []byte, billingCtx paystack.EventContext, ignored bool) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	eventStatus := "received"
	if ignored {
		eventStatus = "ignored"
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO billing_events(provider,payload_hash,event_type,payload,status) VALUES('paystack',$1,$2,$3,$4) ON CONFLICT(payload_hash) DO UPDATE SET status='received',error_message=NULL WHERE billing_events.status='failed' RETURNING id::text`, hash, event, payload, eventStatus).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if ignored {
		return tx.Commit(ctx)
	}
	accountID := billingCtx.AccountID
	reference := billingCtx.Reference
	subCode := billingCtx.SubscriptionCode
	subscriptionFromUpgrade := false
	if accountID == "" && reference != "" {
		lookupErr := tx.QueryRow(ctx, `SELECT account_id::text FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&accountID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
	}
	if accountID == "" && subCode != "" {
		lookupErr := tx.QueryRow(ctx, `SELECT account_id::text FROM billing_subscriptions WHERE provider_subscription_code=$1`, subCode).Scan(&accountID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
	}
	if event == "subscription.create" && accountID == "" && billingCtx.CustomerEmail != "" {
		lookupErr := tx.QueryRow(ctx, `SELECT c.account_id::text FROM billing_checkouts c
			JOIN account_memberships m ON m.account_id=c.account_id AND m.role='owner' AND m.deleted_at IS NULL
			JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
			WHERE i.verified_email=$1 AND c.plan IN ('starter','pro','business') AND c.status='successful'
			ORDER BY c.completed_at DESC NULLS LAST,c.created_at DESC LIMIT 1`, strings.ToLower(strings.TrimSpace(billingCtx.CustomerEmail))).Scan(&accountID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
	}
	switch event {
	case "charge.success":
		if reference == "" {
			return fmt.Errorf("charge.success missing reference")
		}
		var checkoutAccount, checkoutPlan, checkoutCurrency, checkoutType, checkoutMode, checkoutStatus string
		var checkoutAmount int64
		if err = tx.QueryRow(ctx, `SELECT account_id::text,plan,amount_minor,currency,checkout_type,COALESCE(billing_mode,'auto'),status FROM billing_checkouts WHERE provider_reference=$1 FOR UPDATE`, reference).Scan(&checkoutAccount, &checkoutPlan, &checkoutAmount, &checkoutCurrency, &checkoutType, &checkoutMode, &checkoutStatus); err != nil {
			return fmt.Errorf("charge.success checkout lookup: %w", err)
		}
		if accountID == "" {
			accountID = checkoutAccount
		}
		pricing, validPlan := a.checkoutTerms(ctx, reference, checkoutPlan, checkoutCurrency, tx)
		expectedPlanCode := expectedPlanCodeFor(checkoutMode, checkoutType, pricing.Code)
		if !validPlan || !paystack.ValidateChargeContext(billingCtx, checkoutAccount, checkoutAmount, checkoutCurrency, expectedPlanCode) {
			return fmt.Errorf("charge.success failed invariant checks")
		}
		txID := ""
		if billingCtx.TransactionID > 0 {
			txID = fmt.Sprintf("%d", billingCtx.TransactionID)
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_checkouts SET status='successful',provider_transaction_id=COALESCE(NULLIF($2,''),provider_transaction_id),completed_at=COALESCE(completed_at,now()),updated_at=now() WHERE provider_reference=$1`, reference, txID); err != nil {
			return err
		}
		if checkoutType == "subscription" {
			if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, checkoutAccount, checkoutPlan); err != nil {
				return err
			}
		}
		if checkoutStatus != "successful" && checkoutMode == billingModeManual && checkoutType != "upgrade" {
			if err = a.applyManualCheckoutActivation(ctx, tx, reference, checkoutAccount, checkoutPlan, checkoutType); err != nil {
				return err
			}
		}
	case "subscription.create":
		if accountID == "" {
			return fmt.Errorf("subscription.create account linkage unavailable")
		}
		var subscriptionPlan string
		if billingCtx.PlanCode != "" {
			for _, candidate := range []string{"starter", "pro", "business"} {
				if candidatePricing, ok := a.paidPlan(candidate, billingCtx.Currency); ok && candidatePricing.Code != "" && candidatePricing.Code == billingCtx.PlanCode {
					subscriptionPlan = candidate
					break
				}
			}
		}
		if subscriptionPlan == "" && accountID != "" {
			var checkoutType string
			_ = tx.QueryRow(ctx, `SELECT plan,checkout_type FROM billing_checkouts WHERE account_id=$1 AND status='successful' ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, accountID).Scan(&subscriptionPlan, &checkoutType)
			subscriptionFromUpgrade = checkoutType == "upgrade"
		} else if accountID != "" {
			var checkoutType string
			if queryErr := tx.QueryRow(ctx, `SELECT checkout_type FROM billing_checkouts WHERE account_id=$1 AND plan=$2 AND status='successful' ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, accountID, subscriptionPlan).Scan(&checkoutType); queryErr == nil {
				subscriptionFromUpgrade = checkoutType == "upgrade"
			}
		}
		pricing, validPlan := a.paidPlan(subscriptionPlan, billingCtx.Currency)
		var termsReference, termsPlan string
		if queryErr := tx.QueryRow(ctx, `SELECT provider_reference,plan FROM billing_checkouts WHERE account_id=$1 AND currency=$2 AND status='successful' AND (provider_plan_code=$3 OR (provider_plan_code IS NULL AND plan=$4)) ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, accountID, billingCtx.Currency, billingCtx.PlanCode, subscriptionPlan).Scan(&termsReference, &termsPlan); queryErr == nil {
			subscriptionPlan = termsPlan
			pricing, validPlan = a.checkoutTerms(ctx, termsReference, termsPlan, billingCtx.Currency, tx)
		}
		if !validPlan || !paystack.ValidateSubscriptionContext(billingCtx, pricing.Amount, billingCtx.Currency, pricing.Code) {
			return fmt.Errorf("subscription.create failed invariant checks")
		}
		encrypted, encErr := config.Seal(a.cfg.AppEncryptionKey, billingCtx.EmailToken)
		if encErr != nil {
			return encErr
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,provider_plan_code) VALUES($1,'paystack',NULLIF($2,''),$3,$4,$5,'active',$6,$7,$8,$9,$10) ON CONFLICT(provider_subscription_code) DO UPDATE SET status='active',grace_until=NULL,provider_customer_code=COALESCE(EXCLUDED.provider_customer_code,billing_subscriptions.provider_customer_code),provider_email_token_encrypted=EXCLUDED.provider_email_token_encrypted,plan=EXCLUDED.plan,amount_minor=EXCLUDED.amount_minor,currency=EXCLUDED.currency,current_period_start=COALESCE(EXCLUDED.current_period_start,billing_subscriptions.current_period_start),current_period_end=COALESCE(EXCLUDED.current_period_end,billing_subscriptions.current_period_end),provider_plan_code=EXCLUDED.provider_plan_code,updated_at=now() WHERE billing_subscriptions.account_id=EXCLUDED.account_id`, accountID, billingCtx.CustomerCode, subCode, encrypted, subscriptionPlan, billingCtx.Amount, billingCtx.Currency, billingCtx.PeriodStart, billingCtx.PeriodEnd, billingCtx.PlanCode)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("subscription.create account mismatch")
		}
		if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active' AND EXISTS(SELECT 1 FROM billing_subscriptions WHERE provider_subscription_code=$3 AND effective_at<=now())`, accountID, subscriptionPlan, subCode); err != nil {
			return err
		}
	case "invoice.payment_failed":
		if accountID == "" || subCode == "" {
			return fmt.Errorf("invoice.payment_failed subscription linkage unavailable")
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='past_due',grace_until=COALESCE(grace_until,now()+$1::interval),updated_at=now() WHERE provider_subscription_code=$2`, a.cfg.BillingGracePeriod.String(), subCode); err != nil {
			return err
		}
		if err = a.queueEmailJob(ctx, tx, accountID, "payment_failed", nil); err != nil {
			return err
		}
	case "invoice.update":
		if billingCtx.Paid {
			if accountID == "" || subCode == "" {
				return fmt.Errorf("paid invoice.update subscription linkage unavailable")
			}
			if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='active',grace_until=NULL,current_period_start=COALESCE($2,current_period_start),current_period_end=COALESCE($3,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, subCode, billingCtx.PeriodStart, billingCtx.PeriodEnd); err != nil {
				return err
			}
			var subscriptionPlan string
			if err = tx.QueryRow(ctx, `SELECT plan FROM billing_subscriptions WHERE provider_subscription_code=$1`, subCode).Scan(&subscriptionPlan); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active' AND EXISTS(SELECT 1 FROM billing_subscriptions WHERE provider_subscription_code=$3 AND effective_at<=now())`, accountID, subscriptionPlan, subCode); err != nil {
				return err
			}
		}
	case "subscription.not_renew":
		if accountID == "" || subCode == "" {
			return fmt.Errorf("subscription.not_renew linkage unavailable")
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,updated_at=now() WHERE provider_subscription_code=$1`, subCode); err != nil {
			return err
		}
	case "subscription.expiring_cards":
		for _, code := range billingCtx.ExpiringSubscriptions {
			var subAccountID string
			if err = tx.QueryRow(ctx, `SELECT account_id::text FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&subAccountID); err != nil {
				continue
			}
			if err = a.queueEmailJob(ctx, tx, subAccountID, "subscription_expiring_card", nil); err != nil {
				return err
			}
		}
	case "subscription.disable":
		if accountID == "" || subCode == "" {
			return fmt.Errorf("subscription.disable linkage unavailable")
		}
		localStatus := "canceled"
		if mapped, ok := paystack.MapSubscriptionStatus(billingCtx.InvoiceStatus); ok && (mapped == "canceled" || mapped == "complete") {
			localStatus = mapped
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status=$2,grace_until=NULL,updated_at=now() WHERE provider_subscription_code=$1`, subCode, localStatus); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE accounts SET plan=COALESCE((SELECT plan FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1),'free'),updated_at=now() WHERE id=$1`, accountID); err != nil {
			return err
		}
		if err = a.queueEmailJob(ctx, tx, accountID, "subscription_canceled", nil); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_events SET status='processed',processed_at=now(),account_id=COALESCE($2::uuid,account_id),provider_reference=COALESCE(NULLIF($3,''),provider_reference) WHERE id=$1`, id, nullableUUID(accountID), reference); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if event == "subscription.create" && accountID != "" && !subscriptionFromUpgrade {
		a.retirePreviousSubscriptions(ctx, accountID)
	}
	return nil
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *App) queueEmailJob(ctx context.Context, tx pgx.Tx, accountID, template string, payload map[string]any) error {
	var to string
	if err := tx.QueryRow(ctx, `SELECT i.verified_email FROM account_memberships m
		JOIN principal_identities i ON i.principal_id=m.principal_id AND i.deleted_at IS NULL
		WHERE m.account_id=$1 AND m.role='owner' AND m.status='active' AND m.deleted_at IS NULL AND i.verified_email IS NOT NULL
		ORDER BY m.created_at,i.created_at LIMIT 1`, accountID).Scan(&to); errors.Is(err, pgx.ErrNoRows) || to == "" {
		return nil
	} else if err != nil {
		return err
	}
	raw, _ := json.Marshal(payloadOrEmpty(payload))
	if _, err := tx.Exec(ctx, `INSERT INTO email_jobs(account_id,to_email,template,payload,next_attempt_at) VALUES($1,$2,$3,$4,now())`, accountID, to, template, raw); err != nil {
		return err
	}
	a.notifyEmail()
	return nil
}

func payloadOrEmpty(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func (a *App) billingCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	billingCtx, cancelBilling := dbx.Cleanup(r.Context())
	defer cancelBilling()
	if recoverErr := a.recoverSubscription(r.Context(), p.AccountID.String()); recoverErr != nil {
		a.logError("billing cancel subscription recovery failed", "billing_api", "recover_subscription_before_cancel", recoverErr, "account_id", p.AccountID.String())
	}
	var code, token, billingMode string
	err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,COALESCE(provider_email_token_encrypted,''),COALESCE(billing_mode,'auto') FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due') ORDER BY (billing_mode='comp' AND status='active') DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String()}, &code, &token, &billingMode)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logError("billing cancel subscription lookup failed", "billing_api", "read_subscription_for_cancel", err, "account_id", p.AccountID.String())
		}
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		}
		return
	}
	if billingMode == billingModeComp {
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("billing_complimentary_plan"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "unavailable"), http.StatusSeeOther)
		}
		return
	}
	if billingMode != billingModeManual {
		plain, decryptErr := config.Open(a.cfg.AppEncryptionKey, token)
		if decryptErr != nil {
			a.logError("billing cancel subscription token decrypt failed", "billing_api", "decrypt_subscription_token_for_cancel", decryptErr, "account_id", p.AccountID.String(), "subscription_code", code)
			writeBillingUnavailable(w, r, isJSONAPI(r))
			return
		}
		if err = a.billing.DisableSubscription(billingCtx, code, plain); err != nil {
			a.logError("billing cancel provider disable failed", "billing_api", "disable_provider_subscription", err, "account_id", p.AccountID.String(), "subscription_code", code)
			writeBillingUnavailable(w, r, isJSONAPI(r))
			return
		}
	}
	_, err = a.tenantExec(r.Context(), p.AccountID.String(), `UPDATE billing_subscriptions SET cancel_at_period_end=true,status='non_renewing',updated_at=now() WHERE provider_subscription_code=$1 AND account_id=$2`, code, p.AccountID.String())
	if err != nil {
		a.logError("billing cancel local state update failed", "billing_api", "persist_cancelled_subscription", err, "account_id", p.AccountID.String(), "subscription_code", code)
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	if isJSONAPI(r) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"status": "non_renewing"}})
		return
	}
	http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
}

func (a *App) managePayment(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBillingManager(w, r)
	if !ok {
		return
	}
	if recoverErr := a.recoverSubscription(r.Context(), p.AccountID.String()); recoverErr != nil {
		a.logError("billing management subscription recovery failed", "billing_api", "recover_subscription_before_manage", recoverErr, "account_id", p.AccountID.String())
	}
	var code, billingMode string
	err := a.tenantRow(r.Context(), p.AccountID.String(), `SELECT provider_subscription_code,COALESCE(billing_mode,'auto') FROM billing_subscriptions WHERE account_id=$1 AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,created_at DESC LIMIT 1`, []any{p.AccountID.String()}, &code, &billingMode)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logError("billing management subscription lookup failed", "billing_api", "read_subscription_for_management", err, "account_id", p.AccountID.String())
		}
		if isJSONAPI(r) {
			httpx.WriteError(w, r, eToHTTP("invalid_request"))
		} else {
			http.Redirect(w, r, billingRedirect(a.cfg.DashboardOrigin, "pending"), http.StatusSeeOther)
		}
		return
	}
	if billingMode == billingModeComp {
		httpx.WriteError(w, r, eToHTTP("billing_complimentary_plan"))
		return
	}
	if billingMode == billingModeManual {
		httpx.WriteError(w, r, eToHTTP("invalid_request"))
		return
	}
	managementURL, err := a.billing.ManagementLink(r.Context(), code)
	if err != nil {
		a.logError("billing management link creation failed", "billing_api", "create_management_link", err, "account_id", p.AccountID.String(), "subscription_code", code)
		writeBillingUnavailable(w, r, isJSONAPI(r))
		return
	}
	if isJSONAPI(r) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"management_url": managementURL}})
		return
	}
	http.Redirect(w, r, managementURL, http.StatusSeeOther)
}

func (a *App) recoverSubscription(ctx context.Context, accountID string) error {
	var billingMode string
	if err := a.tenantRow(ctx, accountID, `SELECT COALESCE(billing_mode,'auto') FROM billing_checkouts WHERE account_id=$1 AND status='successful' ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, []any{accountID}, &billingMode); err != nil {
		return fmt.Errorf("subscription recovery read successful checkout account_id=%s: %w", accountID, err)
	}
	if billingMode == billingModeManual {
		return nil
	}
	var reference, checkoutPlan, currency, checkoutType string
	var amount int64
	if err := a.tenantRow(ctx, accountID, `SELECT provider_reference,plan,amount_minor,currency,checkout_type FROM billing_checkouts WHERE account_id=$1 AND status='successful' ORDER BY completed_at DESC NULLS LAST,created_at DESC LIMIT 1`, []any{accountID}, &reference, &checkoutPlan, &amount, &currency, &checkoutType); err != nil {
		return fmt.Errorf("subscription recovery read successful checkout account_id=%s: %w", accountID, err)
	}
	checkoutPricing, validPlan := a.checkoutTerms(ctx, reference, checkoutPlan, currency)
	if !validPlan {
		return fmt.Errorf("subscription recovery has unknown plan account_id=%s plan=%s checkout_type=%s", accountID, checkoutPlan, checkoutType)
	}
	transaction, err := a.billing.Verify(ctx, reference)
	if err != nil {
		return fmt.Errorf("subscription recovery verify checkout reference=%s: %w", reference, err)
	}
	expectedPlanCode := checkoutPricing.Code
	if checkoutType == "upgrade" {
		expectedPlanCode = ""
	}
	if !paystack.ValidatePayment(transaction, accountID, amount, currency, expectedPlanCode, checkoutPlan) || transaction.Customer.ID <= 0 {
		return fmt.Errorf("subscription recovery failed transaction checks account_id=%s reference=%s expected_amount_minor=%d expected_currency=%s customer_id=%d", accountID, reference, amount, currency, transaction.Customer.ID)
	}
	subscriptions, err := a.billing.ListSubscriptions(ctx, transaction.Customer.ID)
	if err != nil {
		return fmt.Errorf("subscription recovery list provider subscriptions customer_id=%d: %w", transaction.Customer.ID, err)
	}
	var matched *paystack.Subscription
	var matchedPlan string
	var matchedPricing config.PlanPricing
	var localStatus string
	matchingSubscriptions := 0
	for index := range subscriptions {
		subscription := &subscriptions[index]
		status, known := paystack.MapSubscriptionStatus(subscription.Status)
		if !known || (status != "active" && status != "past_due" && status != "non_renewing") {
			continue
		}
		plan, pricing, ok := a.providerSubscriptionPlan(*subscription)
		if subscription.Plan.Code == checkoutPricing.Code {
			plan, pricing, ok = checkoutPlan, checkoutPricing, true
		}
		if !ok || !validFetchedSubscription(*subscription, pricing.Amount, currency, pricing.Code) || subscription.SubscriptionCode == "" || subscription.EmailToken == "" {
			continue
		}
		if transaction.Customer.CustomerCode != "" && subscription.Customer.CustomerCode != transaction.Customer.CustomerCode {
			continue
		}
		matchingSubscriptions++
		if matched != nil && !newerSubscription(*subscription, *matched) {
			continue
		}
		matched, matchedPlan, matchedPricing, localStatus = subscription, plan, pricing, status
	}
	if matched == nil {
		return fmt.Errorf("subscription recovery found no matching provider subscription account_id=%s provider_subscription_count=%d", accountID, len(subscriptions))
	}
	if matchingSubscriptions > 1 {
		a.logWarn("subscription recovery selected latest provider subscription", "billing_recovery", "select_latest_provider_subscription", nil, "account_id", accountID, "subscription_code", matched.SubscriptionCode, "matching_subscriptions", matchingSubscriptions)
	}
	encrypted, err := config.Seal(a.cfg.AppEncryptionKey, matched.EmailToken)
	if err != nil {
		return fmt.Errorf("subscription recovery encrypt provider token subscription_code=%s: %w", matched.SubscriptionCode, err)
	}
	providerAmount := matched.Amount
	if providerAmount == 0 {
		providerAmount = matched.Plan.Amount
	}
	providerCurrency := matched.Currency
	if providerCurrency == "" {
		providerCurrency = matched.Plan.Currency
	}
	var graceUntil *time.Time
	if localStatus == "past_due" {
		grace := time.Now().Add(a.cfg.BillingGracePeriod)
		graceUntil = &grace
	}
	periodStart := subscriptionStartTime(*matched)
	periodEnd := paystack.ParseTime(matched.NextPaymentDate)
	if providerAmount != matchedPricing.Amount {
		return fmt.Errorf("subscription recovery provider amount mismatch account_id=%s subscription_code=%s provider_amount_minor=%d expected_amount_minor=%d", accountID, matched.SubscriptionCode, providerAmount, matchedPricing.Amount)
	}
	affected, err := a.tenantExec(ctx, accountID, `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,grace_until,cancel_at_period_end) VALUES($1,'paystack',NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(provider_subscription_code) DO UPDATE SET status=EXCLUDED.status,provider_customer_code=COALESCE(EXCLUDED.provider_customer_code,billing_subscriptions.provider_customer_code),provider_email_token_encrypted=EXCLUDED.provider_email_token_encrypted,plan=EXCLUDED.plan,amount_minor=EXCLUDED.amount_minor,currency=EXCLUDED.currency,current_period_start=COALESCE(EXCLUDED.current_period_start,billing_subscriptions.current_period_start),current_period_end=COALESCE(EXCLUDED.current_period_end,billing_subscriptions.current_period_end),grace_until=EXCLUDED.grace_until,cancel_at_period_end=EXCLUDED.cancel_at_period_end,updated_at=now() WHERE billing_subscriptions.account_id=EXCLUDED.account_id`, accountID, matched.Customer.CustomerCode, matched.SubscriptionCode, encrypted, matchedPlan, localStatus, providerAmount, providerCurrency, periodStart, periodEnd, graceUntil, localStatus == "non_renewing")
	if err != nil {
		return fmt.Errorf("subscription recovery persist subscription account_id=%s subscription_code=%s: %w", accountID, matched.SubscriptionCode, err)
	}
	if affected != 1 {
		return fmt.Errorf("subscription recovery account mismatch account_id=%s subscription_code=%s", accountID, matched.SubscriptionCode)
	}
	if _, err = a.tenantExec(ctx, accountID, `UPDATE billing_subscriptions SET provider_plan_code=$2 WHERE provider_subscription_code=$1 AND account_id=$3`, matched.SubscriptionCode, matchedPricing.Code, accountID); err != nil {
		return err
	}
	if _, err = a.tenantExec(ctx, accountID, `UPDATE accounts SET plan=$2,updated_at=now() WHERE id=$1 AND status='active'`, accountID, matchedPlan); err != nil {
		return fmt.Errorf("subscription recovery update account plan account_id=%s plan=%s: %w", accountID, matchedPlan, err)
	}
	return nil
}

func subscriptionCurrency(subscription paystack.Subscription) string {
	if subscription.Currency != "" {
		return subscription.Currency
	}
	return subscription.Plan.Currency
}

func (a *App) providerSubscriptionPlan(subscription paystack.Subscription) (string, config.PlanPricing, bool) {
	for _, plan := range []string{"starter", "pro", "business"} {
		pricing, ok := a.paidPlan(plan, subscriptionCurrency(subscription))
		if ok && pricing.Amount > 0 && pricing.Code != "" && subscription.Plan.Code == pricing.Code {
			return plan, pricing, true
		}
	}
	return "", config.PlanPricing{}, false
}

func newerSubscription(candidate, current paystack.Subscription) bool {
	candidateCreated := paystack.ParseTime(candidate.CreatedAt)
	currentCreated := paystack.ParseTime(current.CreatedAt)
	if candidateCreated != nil || currentCreated != nil {
		if currentCreated == nil {
			return true
		}
		if candidateCreated == nil {
			return false
		}
		if candidateCreated.After(*currentCreated) {
			return true
		}
		if candidateCreated.Before(*currentCreated) {
			return false
		}
	}
	if candidate.Start != current.Start {
		return candidate.Start > current.Start
	}
	candidateNext := paystack.ParseTime(candidate.NextPaymentDate)
	currentNext := paystack.ParseTime(current.NextPaymentDate)
	if candidateNext != nil || currentNext != nil {
		if currentNext == nil {
			return true
		}
		if candidateNext == nil {
			return false
		}
		if candidateNext.After(*currentNext) {
			return true
		}
		if candidateNext.Before(*currentNext) {
			return false
		}
	}
	return candidate.SubscriptionCode > current.SubscriptionCode
}

func subscriptionStartTime(subscription paystack.Subscription) *time.Time {
	if subscription.Start <= 0 {
		return nil
	}
	start := time.Unix(subscription.Start, 0).UTC()
	return &start
}

func (a *App) billingWorker(ctx context.Context) {
	grace := time.NewTicker(15 * time.Minute)
	defer grace.Stop()
	reconcile := time.NewTicker(6 * time.Hour)
	defer reconcile.Stop()
	bounded := func(run func(context.Context)) {
		tickCtx, cancel := context.WithTimeout(ctx, billingTimeout)
		defer cancel()
		run(tickCtx)
	}
	for {
		select {
		case <-grace.C:
			bounded(a.applyScheduledPlanChanges)
			bounded(a.expireBillingGrace)
			bounded(a.remindManualRenewals)
		case <-reconcile.C:
			bounded(a.applyScheduledPlanChanges)
			bounded(a.reconcileBilling)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) remindManualRenewals(ctx context.Context) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.logError("manual renewal reminder transaction begin failed", "billing_worker", "begin_renewal_reminder_transaction", err)
		return
	}
	defer dbx.Rollback(ctx, tx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('monitor:billing-renewal-reminder',0))`).Scan(&locked); err != nil {
		a.logError("manual renewal reminder lock failed", "billing_worker", "acquire_renewal_reminder_lock", err)
		return
	}
	if !locked {
		a.logWarn("manual renewal reminder skipped because another worker holds the lock", "billing_worker", "acquire_renewal_reminder_lock", nil, "skipped", true)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='past_due',grace_until=COALESCE(grace_until,now()+$1::interval),updated_at=now() WHERE billing_mode='manual' AND status='active' AND current_period_end IS NOT NULL AND current_period_end<=now()`, a.cfg.BillingGracePeriod.String()); err != nil {
		a.logError("manual renewal grace transition failed", "billing_worker", "expire_manual_subscription", err)
		return
	}
	rows, err := tx.Query(ctx, `SELECT account_id::text,plan FROM billing_subscriptions WHERE billing_mode='manual' AND status='active' AND current_period_end IS NOT NULL AND current_period_end>now() AND current_period_end<=now()+interval '3 days' AND (renewal_reminded_at IS NULL OR renewal_reminded_at<current_period_start)`)
	if err != nil {
		a.logError("manual renewal reminder query failed", "billing_worker", "query_renewal_reminders", err)
		return
	}
	type reminder struct {
		accountID string
		plan      string
	}
	var reminders []reminder
	for rows.Next() {
		var accountID, plan string
		if err := rows.Scan(&accountID, &plan); err != nil {
			rows.Close()
			a.logError("manual renewal reminder scan failed", "billing_worker", "scan_renewal_reminder", err)
			return
		}
		reminders = append(reminders, reminder{accountID: accountID, plan: plan})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		a.logError("manual renewal reminder iteration failed", "billing_worker", "iterate_renewal_reminders", err)
		return
	}
	for _, reminder := range reminders {
		payLink := strings.TrimRight(a.cfg.DashboardOrigin, "/") + "/app/billing?renew=" + url.QueryEscape(reminder.plan)
		if err = a.queueEmailJob(ctx, tx, reminder.accountID, "billing_renewal", map[string]any{"pay_link": payLink, "plan_name": planDisplayName(reminder.plan)}); err != nil {
			a.logError("manual renewal reminder email enqueue failed", "billing_worker", "enqueue_renewal_reminder", err, "account_id", reminder.accountID, "plan", reminder.plan)
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET renewal_reminded_at=now(),updated_at=now() WHERE account_id=$1 AND plan=$2 AND billing_mode='manual'`, reminder.accountID, reminder.plan); err != nil {
			a.logError("manual renewal reminder marker update failed", "billing_worker", "mark_renewal_reminder_sent", err, "account_id", reminder.accountID, "plan", reminder.plan)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		a.logError("manual renewal reminder commit failed", "billing_worker", "commit_renewal_reminder", err)
	}
}

func planDisplayName(plan string) string {
	switch plan {
	case "starter":
		return "Starter"
	case "business":
		return "Business"
	case "pro":
		return "Pro"
	default:
		return plan
	}
}

func (a *App) expireBillingGrace(ctx context.Context) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.logError("billing grace transaction begin failed", "billing_worker", "begin_grace_transaction", err)
		return
	}
	defer dbx.Rollback(ctx, tx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('monitor:billing-grace',0))`).Scan(&locked); err != nil {
		a.logError("billing grace lock failed", "billing_worker", "acquire_grace_lock", err)
		return
	}
	if !locked {
		a.logWarn("billing grace expiry skipped because another worker holds the lock", "billing_worker", "acquire_grace_lock", nil, "skipped", true)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts a SET plan='free',updated_at=now() FROM billing_subscriptions s WHERE s.account_id=a.id AND s.status='past_due' AND s.grace_until<=now() AND a.plan IN ('starter','pro','business') AND NOT EXISTS(SELECT 1 FROM billing_subscriptions c WHERE c.account_id=a.id AND c.billing_mode='comp' AND c.status='active')`); err != nil {
		a.logError("billing grace account update failed", "billing_worker", "expire_billing_grace", err)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='canceled',grace_until=NULL,updated_at=now() WHERE billing_mode='manual' AND status='past_due' AND grace_until<=now()`); err != nil {
		a.logError("billing grace manual subscription cleanup failed", "billing_worker", "expire_manual_subscription_grace", err)
		return
	}
	if err = a.expireComplimentaryPlans(ctx, tx); err != nil {
		a.logError("complimentary plan expiry failed", "billing_worker", "expire_complimentary_plans", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		a.logError("billing grace commit failed", "billing_worker", "commit_grace_period_update", err)
	}
}

func (a *App) expireComplimentaryPlans(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `UPDATE billing_subscriptions SET status='canceled',updated_at=now() WHERE billing_mode='comp' AND status='active' AND current_period_end IS NOT NULL AND current_period_end<=now() RETURNING account_id::text`)
	if err != nil {
		return err
	}
	accountIDs := make([]string, 0)
	for rows.Next() {
		var accountID string
		if err = rows.Scan(&accountID); err != nil {
			rows.Close()
			return err
		}
		accountIDs = append(accountIDs, accountID)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, accountID := range accountIDs {
		var plan string
		if err = tx.QueryRow(ctx, `UPDATE accounts SET plan=COALESCE((SELECT plan FROM billing_subscriptions WHERE account_id=$1 AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY (billing_mode='comp' AND status='active') DESC,effective_at DESC,created_at DESC LIMIT 1),'free'),updated_at=now() WHERE id=$1 RETURNING plan`, accountID).Scan(&plan); err != nil {
			return err
		}
		a.logEvent("complimentary plan expired", "billing_worker", "expire_complimentary_plan", "account_id", accountID, "plan", plan)
	}
	return nil
}

func (a *App) reconcileBilling(ctx context.Context) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		a.logError("billing reconciliation connection acquire failed", "billing_worker", "acquire_reconciliation_connection", err)
		return
	}
	defer conn.Release()
	locked, err := acquireAdvisoryLock(ctx, conn, "monitor:billing-reconciliation")
	if err != nil || !locked {
		if err != nil {
			a.logError("billing reconciliation lock failed", "billing_worker", "acquire_reconciliation_lock", err)
		} else {
			a.logWarn("billing reconciliation skipped because another worker holds the lock", "billing_worker", "acquire_reconciliation_lock", nil, "skipped", true)
		}
		return
	}
	defer func() {
		if err := releaseAdvisoryLock(ctx, conn, "monitor:billing-reconciliation"); err != nil {
			a.logError("billing reconciliation lock release failed", "billing_worker", "release_reconciliation_lock", err)
		}
	}()
	a.applyScheduledPlanChanges(ctx)
	a.reconcileSubscriptions(ctx)
	a.reconcilePendingCheckouts(ctx)
	a.reconcileUpgradeCheckouts(ctx)
}

func acquireAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, key string) (bool, error) {
	var ok bool
	err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&ok)
	return ok, err
}

func releaseAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, key string) error {
	_, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key)
	return err
}

func (a *App) reconcileSubscriptions(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT provider_subscription_code,plan,currency FROM billing_subscriptions WHERE effective_at<=now() AND billing_mode='auto' AND status IN ('active','past_due','non_renewing')`)
	if err != nil {
		a.logError("billing reconcile subscriptions query failed", "billing_worker", "query_subscriptions_for_reconciliation", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var code, plan, currency string
		if err := rows.Scan(&code, &plan, &currency); err != nil {
			a.logError("billing reconcile subscription scan failed", "billing_worker", "scan_subscription_for_reconciliation", err)
			return
		}
		sub, fetchErr := a.billing.FetchSubscription(ctx, code)
		if fetchErr != nil {
			a.metrics.paystackFailures.Add(1)
			a.logError("billing reconcile fetch subscription failed", "billing_worker", "fetch_provider_subscription", fetchErr, "subscription_code", code, "local_plan", plan)
			continue
		}
		localStatus, ok := paystack.MapSubscriptionStatus(sub.Status)
		if !ok {
			continue
		}
		pricing, validPlan := a.subscriptionTerms(ctx, code, plan, currency)
		if (localStatus == "active" || localStatus == "past_due" || localStatus == "non_renewing") && (!validPlan || !validFetchedSubscription(sub, pricing.Amount, currency, pricing.Code)) {
			a.logError("billing reconcile subscription invariant mismatch", "billing_worker", "validate_provider_subscription", fmt.Errorf("provider subscription does not match local plan invariant"), "subscription_code", code, "local_plan", plan, "provider_status", sub.Status, "provider_plan_code", sub.Plan.Code)
			continue
		}
		if err := a.applyReconciledSubscription(ctx, code, localStatus, sub); err != nil {
			a.logError("billing reconcile state update failed", "billing_worker", "persist_reconciled_subscription", err, "subscription_code", code, "local_status", localStatus, "provider_status", sub.Status)
		}
	}
	if err := rows.Err(); err != nil {
		a.logError("billing reconcile subscriptions iteration failed", "billing_worker", "iterate_subscription_reconciliation", err)
	}
}

func validFetchedSubscription(sub paystack.Subscription, amount int64, currency, expectedPlanCode string) bool {
	providerAmount := sub.Amount
	if providerAmount == 0 {
		providerAmount = sub.Plan.Amount
	}
	providerCurrency := sub.Currency
	if providerCurrency == "" {
		providerCurrency = sub.Plan.Currency
	}
	if providerAmount != amount || providerCurrency != currency {
		return false
	}
	return expectedPlanCode == "" || sub.Plan.Code == expectedPlanCode
}

func (a *App) applyReconciledSubscription(ctx context.Context, code, status string, sub paystack.Subscription) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)
	nextPayment := paystack.ParseTime(sub.NextPaymentDate)
	switch status {
	case "active":
		_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='active',grace_until=NULL,cancel_at_period_end=false,current_period_end=COALESCE($2,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, code, nextPayment)
	case "past_due":
		_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='past_due',grace_until=COALESCE(grace_until,now()+$2::interval),current_period_end=COALESCE($3,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, code, a.cfg.BillingGracePeriod.String(), nextPayment)
	case "non_renewing":
		_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status='non_renewing',cancel_at_period_end=true,current_period_end=COALESCE($2,current_period_end),updated_at=now() WHERE provider_subscription_code=$1`, code, nextPayment)
	case "canceled", "complete":
		_, err = tx.Exec(ctx, `UPDATE billing_subscriptions SET status=$2,grace_until=NULL,updated_at=now() WHERE provider_subscription_code=$1`, code, status)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE accounts SET plan=COALESCE((SELECT plan FROM billing_subscriptions WHERE account_id=(SELECT account_id FROM billing_subscriptions WHERE provider_subscription_code=$1) AND effective_at<=now() AND status IN ('active','past_due','non_renewing') ORDER BY effective_at DESC,created_at DESC LIMIT 1),'free'),updated_at=now() WHERE id=(SELECT account_id FROM billing_subscriptions WHERE provider_subscription_code=$1) AND status='active'`, code)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) reconcilePendingCheckouts(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT provider_reference,account_id::text,amount_minor,currency,plan,checkout_type,COALESCE(billing_mode,'auto') FROM billing_checkouts WHERE status IN ('pending','initialized') AND created_at>now()-interval '7 days'`)
	if err != nil {
		a.logError("billing reconcile checkouts query failed", "billing_worker", "query_checkouts_for_reconciliation", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var reference, accountID, currency, plan, checkoutType, billingMode string
		var amount int64
		if err := rows.Scan(&reference, &accountID, &amount, &currency, &plan, &checkoutType, &billingMode); err != nil {
			a.logError("billing reconcile checkout scan failed", "billing_worker", "scan_checkout_for_reconciliation", err)
			return
		}
		transaction, verifyErr := a.billing.Verify(ctx, reference)
		if verifyErr != nil {
			a.metrics.paystackFailures.Add(1)
			a.logError("billing reconcile checkout verification failed", "billing_worker", "verify_pending_checkout", verifyErr, "reference", reference, "account_id", accountID, "plan", plan, "checkout_type", checkoutType)
			continue
		}
		switch transaction.Status {
		case "success":
			pricing, validPlan := a.checkoutTerms(ctx, reference, plan, currency)
			expectedPlanCode := expectedPlanCodeFor(billingMode, checkoutType, pricing.Code)
			if !validPlan || !paystack.ValidatePayment(transaction, accountID, amount, currency, expectedPlanCode, plan) {
				continue
			}
			if err := a.activateCheckout(ctx, reference, accountID, transaction.ID); err != nil {
				a.logError("billing reconcile checkout activation failed", "billing_worker", "activate_reconciled_checkout", err, "reference", reference, "account_id", accountID, "plan", plan, "checkout_type", checkoutType, "amount_minor", amount, "currency", currency)
				continue
			}
			if checkoutType == "upgrade" {
				if err := a.completeUpgradeCheckout(ctx, reference, transaction.Customer.CustomerCode); err != nil {
					a.logError("billing reconcile upgrade provisioning failed", "billing_worker", "provision_reconciled_upgrade", err, "reference", reference, "account_id", accountID, "plan", plan)
				}
			}
		case "failed", "abandoned", "cancelled", "canceled":
			if _, err := a.db.Exec(ctx, `UPDATE billing_checkouts SET status='failed',updated_at=now() WHERE provider_reference=$1 AND status IN ('pending','initialized')`, reference); err != nil {
				a.logError("billing reconcile checkout fail mark failed", "billing_worker", "mark_reconciled_checkout_failed", err, "reference", reference, "account_id", accountID, "checkout_type", checkoutType)
			}
		}
	}
	if err := rows.Err(); err != nil {
		a.logError("billing reconcile checkouts iteration failed", "billing_worker", "iterate_checkout_reconciliation", err)
	}
}

func (a *App) reconcileUpgradeCheckouts(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT provider_reference FROM billing_checkouts WHERE checkout_type='upgrade' AND status='successful' AND provisioned_at IS NULL AND created_at>now()-interval '7 days'`)
	if err != nil {
		a.logError("billing reconcile upgrades query failed", "billing_worker", "query_unprovisioned_upgrades", err)
		return
	}
	var references []string
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			rows.Close()
			a.logError("billing reconcile upgrade scan failed", "billing_worker", "scan_unprovisioned_upgrade", err)
			return
		}
		references = append(references, reference)
	}
	rows.Close()
	for _, reference := range references {
		if _, err := a.verifyBillingCheckout(ctx, reference, ""); err != nil {
			a.logError("billing reconcile upgrade retry failed", "billing_worker", "retry_upgrade_verification", err, "reference", reference)
		}
	}
}
