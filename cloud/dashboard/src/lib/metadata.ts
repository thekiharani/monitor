import { useEffect } from "react";
import { useLocation } from "react-router-dom";

const SITE_NAME = "Noria Monitor";
const SITE_ORIGIN = "https://monitor.noria.co.ke";
const SOCIAL_IMAGE = `${SITE_ORIGIN}/og-image.jpg`;
const DEFAULT_METADATA: PageMetadata = {
  title: "Dashboard",
  description: "Manage your monitors, status pages, team, and billing in Noria Monitor.",
};

export interface PageMetadata {
  title: string;
  description: string;
  robots?: string;
}

const CUSTOMER_PAGES: Record<string, PageMetadata> = {
  "/app": {
    title: "Overview",
    description:
      "See which of your services are up, how fast they respond, and what needs attention.",
  },
  "/app/monitors": {
    title: "Monitors",
    description: "Every endpoint Noria Monitor checks for you, with its state and response time.",
  },
  "/app/monitors/new": {
    title: "Add monitor",
    description: "Choose what to check, how often, and from which regions.",
  },
  "/app/incidents": {
    title: "Incidents",
    description:
      "Open outages to acknowledge, and the history of every outage and how long it lasted.",
  },
  "/app/status-pages": {
    title: "Status pages",
    description:
      "Public pages that tell your customers what is working, with incident notices and uptime history.",
  },
  "/app/alerts": {
    title: "Alerts",
    description: "Where alerts go when a monitor fails, and maintenance windows that silence them.",
  },
  "/app/api-keys": {
    title: "API Keys",
    description:
      "Manage the credentials used by your backend servers, workers, and automated scripts.",
  },
  "/app/usage": {
    title: "Usage & quotas",
    description:
      "Review API request, API key, and team member usage for your current billing period.",
  },
  "/app/billing": {
    title: "Billing & subscription",
    description:
      "Manage your Noria Monitor subscription plan, payment details, and billing settings.",
  },
  "/app/account": {
    title: "Account preferences",
    description: "Manage your account profile, theme settings, and security status.",
  },
};

const CONTROL_PAGES: Record<string, PageMetadata> = {
  overview: {
    title: "Operations overview",
    description: "Platform totals and the tenants that need attention.",
  },
  accounts: {
    title: "Accounts",
    description: "Open a tenant workspace or change lifecycle state with an auditable reason.",
  },
  usage: {
    title: "Usage",
    description: "Review monthly API request counters by account.",
  },
  plans: {
    title: "Plan management",
    description: "Manage plan pricing, limits, and customer plan assignments.",
  },
  billing: {
    title: "Billing operations",
    description: "Review subscriptions, checkouts, and provider events across customer accounts.",
  },
  credentials: {
    title: "Credentials",
    description: "Inspect audience, assurance, and revocation state without exposing secrets.",
  },
  jobs: {
    title: "Email jobs",
    description: "Inspect delivery backlogs and requeue failed work.",
  },
  access: {
    title: "Access grants",
    description: "Delegate atomic capabilities with explicit scope and reason.",
  },
  audit: {
    title: "Audit trail",
    description: "Review append-only evidence of control-plane decisions.",
  },
};

export function getPageMetadata(pathname: string): PageMetadata {
  if (pathname === "/login") {
    return {
      title: "Sign in",
      description: "Secure passwordless access to your Noria Monitor workspace.",
    };
  }

  if (pathname === "/control" || pathname === "/control/") {
    return CONTROL_PAGES.overview!;
  }

  if (pathname.startsWith("/control/")) {
    const section = pathname.split("/")[2];
    return (
      (CONTROL_PAGES as Record<string, PageMetadata | undefined>)[section ?? ""] ??
      CONTROL_PAGES.overview!
    );
  }

  if (pathname.startsWith("/app/status-pages/")) {
    return {
      title: "Status page",
      description: "Components, notices, appearance and the custom domain of this status page.",
    };
  }

  if (!CUSTOMER_PAGES[pathname] && pathname.startsWith("/app/monitors/")) {
    return {
      title: "Monitor",
      description: "Response times, daily uptime, and recent checks for this monitor.",
    };
  }

  return CUSTOMER_PAGES[pathname] ?? DEFAULT_METADATA;
}

function setMetaTag(key: string, content: string, attribute: "name" | "property" = "name") {
  let tag = document.head.querySelector<HTMLMetaElement>(
    `meta[data-noria-meta="${key}"], meta[${attribute}="${key}"]`,
  );
  if (!tag) {
    tag = document.createElement("meta");
    tag.setAttribute(attribute, key);
    document.head.appendChild(tag);
  }
  tag.dataset.noriaMeta = key;
  tag.setAttribute("content", content);
}

function setCanonical(url: string) {
  let tag = document.head.querySelector<HTMLLinkElement>('link[rel="canonical"]');
  if (!tag) {
    tag = document.createElement("link");
    tag.rel = "canonical";
    document.head.appendChild(tag);
  }
  tag.href = url;
}

export function canonicalURL(pathname: string): string {
  const trimmed = pathname !== "/" ? pathname.replace(/\/+$/, "") : pathname;
  return `${SITE_ORIGIN}${trimmed || "/"}`;
}

export function socialImage(): string {
  return SOCIAL_IMAGE;
}

export function setPageMetadata(metadata: PageMetadata) {
  const title = `${metadata.title} | ${SITE_NAME}`;
  const url = canonicalURL(window.location.pathname);
  const image = socialImage();
  document.title = title;
  setCanonical(url);
  setMetaTag("description", metadata.description);
  setMetaTag("robots", metadata.robots ?? "noindex, nofollow");
  setMetaTag("og:site_name", SITE_NAME, "property");
  setMetaTag("og:title", title, "property");
  setMetaTag("og:description", metadata.description, "property");
  setMetaTag("og:type", "website", "property");
  setMetaTag("og:url", url, "property");
  setMetaTag("og:image", image, "property");
  setMetaTag("og:image:alt", title, "property");
  setMetaTag("og:locale", "en", "property");
  setMetaTag("twitter:card", "summary_large_image", "name");
  setMetaTag("twitter:title", title, "name");
  setMetaTag("twitter:description", metadata.description, "name");
  setMetaTag("twitter:image", image, "name");
}

export function usePageMetadata(metadata?: PageMetadata) {
  const title = metadata?.title;
  const description = metadata?.description;
  const robots = metadata?.robots;
  useEffect(() => {
    if (title === undefined || description === undefined) return;
    setPageMetadata({ title, description, robots });
  }, [title, description, robots]);
}

export function RouteMetadata() {
  const { pathname } = useLocation();
  usePageMetadata(pathname.startsWith("/control") ? undefined : getPageMetadata(pathname));
  return null;
}
