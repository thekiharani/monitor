import { useQuery } from "@tanstack/react-query";
import { api, APIError } from "@/api";

export interface ControlSession {
  principal_id: string;
  account_id: string;
  email: string;
  capabilities: string[];
  known_capabilities: string[];
}

export interface ControlAccount {
  id: string;
  principal_id: string;
  email: string;
  status: string;
  plan: string;
  created_at: unknown;
  active_credentials?: number;
  subscription_status?: string | null;
  subscription_plan?: string | null;
  complimentary_plan?: string | null;
  complimentary_expires_at?: unknown;
  period_api_requests?: number;
}

export interface ControlStats {
  accounts?: number;
  active_credentials?: number;
  pending_jobs?: number;
  past_due_billing?: number;
  active_accounts?: number;
  suspended_accounts?: number;
  accounts_new_30d?: number;
  accounts_closed_30d?: number;
  closed_pending_erasure?: number;
  team_members?: number;
  pending_invites?: number;
  mrr_minor?: number;
  paying_accounts?: number;
  complimentary_accounts?: number;
  failing_jobs?: number;
  plans?: Record<string, number>;
}

export type ControlAdmission =
  | { state: "admitted"; session: ControlSession }
  | { state: "unauthenticated" }
  | { state: "unauthorized" };

export const controlSessionKey = ["control", "session"] as const;

export async function getControlAdmission(): Promise<ControlAdmission> {
  try {
    const body = await api<{ data: ControlSession }>("/api/control/v1/session");
    const data = body.data;
    return {
      state: "admitted",
      session: {
        principal_id: data.principal_id,
        account_id: data.account_id,
        capabilities: data.capabilities ?? [],
        email: data.email ?? "",
        known_capabilities: data.known_capabilities ?? [],
      },
    };
  } catch (error) {
    if (error instanceof APIError && error.status === 401) {
      return { state: "unauthenticated" };
    }
    if (error instanceof APIError && error.status === 403) {
      return { state: "unauthorized" };
    }
    throw error;
  }
}

export function useControlSession() {
  return useQuery({
    queryKey: controlSessionKey,
    queryFn: getControlAdmission,
    staleTime: 60_000,
    retry: false,
  });
}

export function hasControlCapability(
  session: ControlSession | null | undefined,
  capability: string,
) {
  if (!session) return false;
  return session.capabilities.includes("platform.*") || session.capabilities.includes(capability);
}

export type ControlValue =
  | string
  | number
  | boolean
  | null
  | ControlValue[]
  | { [key: string]: ControlValue };

export type ControlRow = Record<string, ControlValue | undefined>;

export function cellText(value: ControlValue | undefined, fallback = "—"): string {
  if (value === null || value === undefined || value === "") return fallback;
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

export function cellString(value: ControlValue | undefined): string {
  return typeof value === "string"
    ? value
    : value === null || value === undefined
      ? ""
      : String(value);
}

export async function controlFetch<T>(path: string, init?: RequestInit): Promise<T> {
  return api<T>(path, init);
}

export function controlListPath(path: string, params: Record<string, string | number | undefined>) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue;
    search.set(key, String(value));
  }
  const query = search.toString();
  return query ? `${path}?${query}` : path;
}
