package httpx

import (
	"crypto/subtle"
	"encoding/json/v2"
	"net/http"
	"strings"
	"time"
	"uuid"
)

type contextKey string

const RequestIDKey contextKey = "request_id"

type Error struct {
	Code, Message string
	Status        int
}

func (e Error) Error() string { return e.Code }

var errorsByCode = map[string]Error{
	"invalid_request": {"invalid_request", "The request is invalid.", 400}, "invalid_json": {"invalid_json", "The request body is not valid JSON.", 400}, "forbidden_role": {"forbidden_role", "Your role does not allow this action.", 403}, "invalid_role": {"invalid_role", "The supplied role is invalid.", 422}, "already_member": {"already_member", "That person is already on this team.", 409}, "invite_pending": {"invite_pending", "An invitation is already pending for that address.", 409}, "invalid_invite": {"invalid_invite", "The invitation is invalid or has expired.", 404}, "invite_wrong_account": {"invite_wrong_account", "This invitation was sent to a different email address. Sign in with that address to accept it.", 403}, "cannot_change_own_role": {"cannot_change_own_role", "You cannot change your own role.", 409}, "owner_cannot_leave": {"owner_cannot_leave", "An owner cannot leave their own account.", 409}, "confirmation_mismatch": {"confirmation_mismatch", "The confirmation text does not match.", 422}, "invalid_display_name": {"invalid_display_name", "The display name must be 1 to 128 characters.", 422}, "invalid_workspace_name": {"invalid_workspace_name", "The workspace name must be 1 to 128 characters.", 422}, "invalid_timezone": {"invalid_timezone", "That is not a recognised time zone.", 422}, "email_taken": {"email_taken", "That email address is already in use.", 409}, "email_unchanged": {"email_unchanged", "That is already your email address.", 409}, "subscription_active": {"subscription_active", "Cancel your subscription before closing the account.", 409}, "not_found": {"not_found", "The requested resource was not found.", 404}, "monitor_not_found": {"monitor_not_found", "The requested monitor was not found.", 404}, "channel_not_found": {"channel_not_found", "The requested alert channel was not found.", 404}, "unauthorized": {"unauthorized", "Authentication is required.", 401}, "cross_origin": {"cross_origin", "This request came from another site, so it was refused.", 403}, "probe_region_unavailable": {"probe_region_unavailable", "This probe's region is not offered to customers. Add it to PROBE_REGIONS on the server.", 409}, "invalid_api_key": {"invalid_api_key", "The API key is invalid.", 401}, "account_suspended": {"account_suspended", "The account is suspended.", 403}, "rate_limited": {"rate_limited", "Too many requests.", 429}, "quota_exceeded": {"quota_exceeded", "The account quota has been exceeded.", 429}, "plan_limit_exceeded": {"plan_limit_exceeded", "The plan limit has been exceeded.", 429}, "internal_error": {"internal_error", "An unexpected error occurred.", 500}, "service_unavailable": {"service_unavailable", "The service is temporarily unavailable.", 503}, "billing_resume_failed": {"billing_resume_failed", "We couldn't schedule your subscription to resume. Your current plan is unchanged. Please try again or update your payment method.", 503}, "billing_plan_change_failed": {"billing_plan_change_failed", "We couldn't schedule that plan change. Your current plan is unchanged. Refresh Billing and try again; if it persists, open Manage Payment & Invoices to check your payment authorization.", 503}, "billing_checkout_pending": {"billing_checkout_pending", "A plan payment is already in progress. Finish it in the payment window, or try again in a moment.", 409}, "billing_checkout_completed": {"billing_checkout_completed", "Your previous payment went through. Refresh Billing to see your new plan.", 409}, "billing_complimentary_plan": {"billing_complimentary_plan", "Your plan is complimentary, so there is nothing to pay or change here. Contact support if you need a different plan.", 409},
}

func NewError(code string) Error {
	if e, ok := errorsByCode[code]; ok {
		return e
	}
	return errorsByCode["internal_error"]
}
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, value)
}
func WriteError(w http.ResponseWriter, r *http.Request, e Error) {
	WriteJSON(w, e.Status, map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "request_id": RequestID(r)}})
}
func RequestID(r *http.Request) string {
	if id, ok := r.Context().Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func SafeRequestID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func NewRequestID() string { return uuid.NewV7().String() }
func RFC3339(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
func HostMatches(r *http.Request, origins ...string) bool {
	host := strings.ToLower(r.Host)
	for _, origin := range origins {
		u := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(origin), "https://"), "http://")
		if i := strings.IndexByte(u, '/'); i >= 0 {
			u = u[:i]
		}
		if host == u {
			return true
		}
	}
	return false
}
