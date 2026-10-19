import type { SeriesRange } from "@/types";

export type IncidentStatus = "open" | "resolved";

export * from "@/types";

const API_ORIGIN = (import.meta.env.VITE_API_ORIGIN ?? "").replace(/\/$/, "");

export const endpoints = {
  auth: {
    requestOtp: "/auth/otp/request",
    verifyOtp: "/auth/otp/verify",
    logout: "/auth/logout",
    signOut: "/auth/session",
  },
  me: "/api/dashboard/v1/me",
  profile: () => "/api/dashboard/v1/me",
  changeEmail: () => "/api/dashboard/v1/me/email",
  verifyEmailChange: () => "/api/dashboard/v1/me/email/verify",
  workspace: () => "/api/dashboard/v1/account",
  usage: "/api/dashboard/v1/usage",
  apiKeys: (id?: string) =>
    id ? `/api/dashboard/v1/api-keys/${encodeURIComponent(id)}` : "/api/dashboard/v1/api-keys",
  billing: "/api/dashboard/v1/billing",
  plans: () => "/api/v1/plans",
  closeAccount: () => "/api/dashboard/v1/account/close",
  members: () => "/api/dashboard/v1/members",
  member: (id: string) => `/api/dashboard/v1/members/${encodeURIComponent(id)}`,
  memberInvites: () => "/api/dashboard/v1/members/invites",
  memberInvite: (id: string) => `/api/dashboard/v1/members/invites/${encodeURIComponent(id)}`,
  memberInviteResend: (id: string) =>
    `/api/dashboard/v1/members/invites/${encodeURIComponent(id)}/resend`,
  acceptInvite: (token: string) =>
    `/api/dashboard/v1/members/invites/${encodeURIComponent(token)}/accept`,
  accounts: () => "/api/dashboard/v1/accounts",
  switchAccount: (id: string) => `/api/dashboard/v1/accounts/${encodeURIComponent(id)}/switch`,
  monitors: () => "/api/dashboard/v1/monitors",
  monitor: (id: string) => `/api/dashboard/v1/monitors/${encodeURIComponent(id)}`,
  monitorResults: (id: string, params: { limit?: number; before?: string; region?: string } = {}) =>
    withQuery(`/api/dashboard/v1/monitors/${encodeURIComponent(id)}/results`, params),
  monitorSeries: (id: string, range: SeriesRange) =>
    withQuery(`/api/dashboard/v1/monitors/${encodeURIComponent(id)}/series`, { range }),
  monitorDaily: (id: string, days?: number) =>
    withQuery(`/api/dashboard/v1/monitors/${encodeURIComponent(id)}/daily`, { days }),
  regions: () => "/api/dashboard/v1/regions",
  alertChannels: () => "/api/dashboard/v1/alert-channels",
  alertChannel: (id: string) => `/api/dashboard/v1/alert-channels/${encodeURIComponent(id)}`,
  alertChannelTest: (id: string) =>
    `/api/dashboard/v1/alert-channels/${encodeURIComponent(id)}/test`,
  alertChannelDeliveries: (id: string) =>
    `/api/dashboard/v1/alert-channels/${encodeURIComponent(id)}/deliveries`,
  incidents: (params: { status?: IncidentStatus; monitor_id?: string; limit?: number } = {}) =>
    withQuery("/api/dashboard/v1/incidents", params),
  incidentAcknowledge: (id: string) =>
    `/api/dashboard/v1/incidents/${encodeURIComponent(id)}/acknowledge`,
  statusPages: () => "/api/dashboard/v1/status-pages",
  statusPage: (id: string) => `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}`,
  statusPageComponents: (id: string) =>
    `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}/components`,
  statusPageVerify: (id: string) =>
    `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}/domain/verify`,
  statusPosts: (id: string) => `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}/posts`,
  statusPost: (id: string, post: string) =>
    `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}/posts/${encodeURIComponent(post)}`,
  statusPostUpdates: (id: string, post: string) =>
    `/api/dashboard/v1/status-pages/${encodeURIComponent(id)}/posts/${encodeURIComponent(post)}/updates`,
  maintenance: (id?: string) =>
    id
      ? `/api/dashboard/v1/maintenance/${encodeURIComponent(id)}`
      : "/api/dashboard/v1/maintenance",
} as const;

function withQuery(path: string, params: Record<string, string | number | undefined>) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}

export class APIError extends Error {
  status: number;
  code: string;
  constructor(message: string, status: number, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

function csrfToken() {
  return (
    document.cookie
      .split("; ")
      .find((part) => part.startsWith("__Host-csrf="))
      ?.slice(12) ?? ""
  );
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = init.method?.toUpperCase() ?? "GET";
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (method !== "GET" && method !== "HEAD") {
    headers.set("X-CSRF-Token", csrfToken());
  }
  const response = await fetch(`${API_ORIGIN}${path}`, {
    ...init,
    headers,
    credentials: "include",
  });
  if (response.status === 204) return undefined as T;
  const body = (await response.json().catch(() => ({}))) as {
    error?: { code?: string; message?: string };
  } & T;
  if (!response.ok) {
    throw new APIError(
      body.error?.message ?? "Something went wrong. Please try again.",
      response.status,
      body.error?.code,
    );
  }
  return body;
}

export const queryKeys = {
  me: ["me"] as const,
  usage: ["usage"] as const,
  keys: ["api-keys"] as const,
  members: ["members"] as const,
  accounts: ["accounts"] as const,
  plans: ["plans"] as const,
  billing: ["billing"] as const,
  monitors: ["monitors"] as const,
  monitor: (id: string) => ["monitors", id] as const,
  monitorResults: (id: string, region: string) => ["monitors", id, "results", region] as const,
  monitorSeries: (id: string, range: SeriesRange) => ["monitors", id, "series", range] as const,
  monitorDaily: (id: string) => ["monitors", id, "daily"] as const,
  regions: ["regions"] as const,
  alertChannels: ["alert-channels"] as const,
  channelDeliveries: (id: string) => ["alert-channels", id, "deliveries"] as const,
  incidents: ["incidents"] as const,
  incidentList: (status: IncidentStatus | "", monitorId: string, limit?: number) =>
    ["incidents", status, monitorId, limit ?? null] as const,
  maintenance: ["maintenance"] as const,
  statusPages: ["status-pages"] as const,
  statusPage: (id: string) => ["status-pages", id] as const,
  statusPosts: (id: string) => ["status-pages", id, "posts"] as const,
};

export const apiOrigin = API_ORIGIN || window.location.origin;
