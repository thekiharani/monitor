# SaaS readiness review — 17 October 2026

The core monitoring product is approximately 90% complete for an MVP. The complete self-service SaaS offering is approximately 75–80% ready for public launch. These are engineering estimates based on the implementation and local validation, rather than a measured project burndown. The project is suitable for product review now; a paid beta needs live billing, notification and hosting acceptance checks first.

| Area | Implemented | Remaining work or evidence |
|---|---|---|
| Monitoring | HTTP, keyword, TCP, heartbeats, TLS-expiry warnings, regional checks, confirmation, history and uptime charts | Verify probes running on independent regional hosts; run sustained load and failure tests |
| Incident response | Incidents, acknowledgement, recovery, maintenance, email, Telegram, Slack and signed webhooks | Exercise real alert delivery and retries against configured accounts |
| Public status pages | Published pages, components/groups, updates, badges and custom domains | Verify customer DNS and certificate issuance on the deployment |
| SaaS accounts | Email-code sign-up/sign-in, workspaces, switching, invitations, roles, API keys, quotas and rate limits | Review the first-user journey; add guided onboarding if the empty states do not get users to their first working monitor |
| Billing | Persisted control-panel plan pricing/limits, audited plan edits and complimentary account assignments; Free/Starter/Pro/Business plans; KES and USD checkout; card subscriptions; KES M-Pesa; prorated upgrades; scheduled downgrades; renewals, cancellation, resume, payment verification and reconciliation | Configure live Paystack keys and currency-specific plans; verify successful and failed card/M-Pesa payments and provider webhooks. Customer invoice/receipt history is not implemented as a dedicated dashboard view |
| Security and operations | Tenant row-level security, CSRF protection, encrypted secrets, capability-controlled operators, audit records, health endpoints, logs, image build and publish workflow, server-managed database backups | Verify the deployed configuration, restore a backup, exercise rollout/rollback, and perform browser/mobile acceptance checks |
| Acquisition and support | Login and authenticated product UI | No public landing/pricing pages, terms/privacy pages, or customer support entry point were found in this repository; verify whether these exist elsewhere |
| Developer experience | Bearer API, CLI and embedded OpenAPI contracts | Dashboard navigation points to `/docs`, and the API redirects there, but the React router has no `/docs` page; repair this link or supply the documentation page |

MFA/SSO, SMS or phone escalation, on-call scheduling, status-page subscriber notifications, and customer report exports were not found. Treat these as later product choices; they are not all required for a focused uptime-monitoring MVP.

KES is the base price. USD uses the fixed product rule USD = KES / 100, rounded to US cents. Default monthly prices are Ksh 500 / $5, Ksh 2,000 / $20, and Ksh 5,000 / $50. Customers choose the currency for their first paid subscription; renewal and plan changes retain it. M-Pesa is KES-only and USD is card-only. Setup is documented in [README.md](README.md).

Validation for this review: Go vet and 408 race-enabled backend test cases passed, including 177 integration cases against a separate PostgreSQL database; dashboard type checks, lint, 53 tests and the production build passed. New billing tests cover USD checkout, mismatched payment currency, USD/M-Pesa rejection, prorated upgrades, renewals, scheduled plan changes, resume, and MRR expressed in KES. The local review database was preserved. These checks do not establish live merchant-account readiness, real regional independence, backup recoverability or browser layout quality.
