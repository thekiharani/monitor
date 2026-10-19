import { toast } from "sonner";
import { APIError } from "@/api";

const ERROR_TOAST_DURATION = 60_000;

const safeAPIErrorMessages: Record<string, string> = {
  invalid_request: "We couldn't complete that request. Please check the details and try again.",
  invalid_json: "The submitted information wasn't valid. Please check it and try again.",
  invalid_url: "Please enter a valid URL and try again.",
  invalid_code: "That code isn't valid. Please check it and try again.",
  code_taken: "That code is already in use. Please choose another one.",
  unauthorized: "Your session has expired. Please sign in again.",
  invalid_display_name: "A display name can be up to 128 characters, on a single line.",
  invalid_workspace_name: "A workspace name can be up to 128 characters, on a single line.",
  invalid_timezone: "That time zone isn't one we recognise. Pick one from the list.",
  email_taken: "That email address is already in use on another Noria Monitor account.",
  email_unchanged: "That is already your email address.",
  forbidden_role: "Your role on this workspace doesn't allow that change.",
  account_suspended: "This account is suspended and cannot perform that action.",
  rate_limited: "Too many requests. Please wait a moment and try again.",
  quota_exceeded: "This account has reached its current usage limit.",
  plan_limit_exceeded: "Your current plan limit prevents that action.",
  billing_resume_failed:
    "We couldn't schedule your subscription to resume. Your current plan is unchanged. Please try again or update your payment method.",
  billing_plan_change_failed:
    "We couldn't schedule that plan change. Your current plan is unchanged. Refresh Billing and try again; if it persists, open Manage Payment & Invoices to check your payment authorization.",
  billing_checkout_pending:
    "A plan payment is already in progress. Complete it in Paystack or wait for it to expire before starting another.",
  service_unavailable: "The service is temporarily unavailable. Please try again in a moment.",
  internal_error: "Something went wrong on our side. Please try again in a moment.",
};

const serverWorded = new Set([
  "invalid_monitor",
  "invalid_channel",
  "invalid_maintenance",
  "invalid_status_page",
  "slug_taken",
  "domain_taken",
  "domain_not_verified",
  "plan_limit_exceeded",
]);

export function errorToast(error: unknown, fallback = "Something went wrong. Please try again.") {
  const message =
    error instanceof APIError
      ? serverWorded.has(error.code) && error.message
        ? error.message
        : (safeAPIErrorMessages[error.code] ?? fallback)
      : fallback;
  return toast.error(message, { duration: ERROR_TOAST_DURATION });
}
