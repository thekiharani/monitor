import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import type { Account, Incident, Monitor, MonitorKind, MonitorState, PublicPlan } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export const MONITOR_REFETCH_MS = 30_000;

export const INTERVAL_OPTIONS = [30, 60, 120, 300, 600, 900, 1800, 3600, 86400];

export const monitorStates: MonitorState[] = ["up", "down", "pending", "paused"];

export const stateLabels: Record<MonitorState, string> = {
  up: "Up",
  down: "Down",
  pending: "Pending",
  paused: "Paused",
};

export const kindLabels: Record<MonitorKind, string> = {
  http: "HTTP",
  keyword: "Keyword",
  tcp: "TCP",
  heartbeat: "Heartbeat",
};

const stateDotClasses: Record<MonitorState, string> = {
  up: "bg-status-up",
  down: "bg-status-down",
  pending: "bg-muted-foreground/50",
  paused: "bg-status-warn",
};

const stateBadgeClasses: Record<MonitorState, string> = {
  up: "border-success/30 bg-success/10 text-success",
  down: "border-destructive/30 bg-destructive/10 text-destructive",
  pending: "border-border bg-muted/60 text-muted-foreground",
  paused: "border-warning/30 bg-warning/10 text-warning",
};

export function MonitorStateDot({ state, className }: { state: MonitorState; className?: string }) {
  return (
    <span className={cn("relative inline-flex size-2.5 shrink-0", className)}>
      {state === "down" && (
        <span className="absolute inset-0 animate-ping rounded-full bg-status-down/60" />
      )}
      <span
        role="img"
        aria-label={stateLabels[state]}
        className={cn("relative inline-flex size-2.5 rounded-full", stateDotClasses[state])}
      />
    </span>
  );
}

export function MonitorStateBadge({ state }: { state: MonitorState }) {
  return (
    <Badge variant="outline" className={cn("gap-1.5", stateBadgeClasses[state])}>
      <span className={cn("size-1.5 rounded-full", stateDotClasses[state])} />
      {stateLabels[state]}
    </Badge>
  );
}

export function formatInterval(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

export function monitorTarget(monitor: Monitor): string {
  return monitor.kind === "heartbeat"
    ? `Pings expected every ${formatInterval(monitor.interval_seconds)}`
    : (monitor.target ?? "");
}

export function formatDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.round(totalSeconds));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

export function formatMs(value: number | null | undefined): string {
  return value === null || value === undefined ? "—" : `${Math.round(value).toLocaleString()} ms`;
}

export function formatUptime(value: number | null | undefined): string {
  if (value === null || value === undefined) return "—";
  return `${value >= 99.995 ? "100" : value.toFixed(2)}%`;
}

export function formatRelative(value: string | null | undefined, now: number): string {
  if (!value) return "Never";
  const seconds = Math.round((now - new Date(value).getTime()) / 1000);
  if (Number.isNaN(seconds)) return "—";
  if (seconds < 0) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function useNow(tickMs = 15_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), tickMs);
    return () => window.clearInterval(timer);
  }, [tickMs]);
  return now;
}

export function useMonitors(enabled = true) {
  return useQuery({
    queryKey: queryKeys.monitors,
    queryFn: () => api<{ data: Monitor[] }>(endpoints.monitors()),
    refetchInterval: MONITOR_REFETCH_MS,
    enabled,
  });
}

export function useAccountPlan(): PublicPlan | undefined {
  const accountQuery = useQuery({
    queryKey: queryKeys.me,
    queryFn: () => api<{ data: { account: Account } }>(endpoints.me),
  });
  const plansQuery = useQuery({
    queryKey: queryKeys.plans,
    queryFn: () => api<{ data: PublicPlan[] }>(endpoints.plans()),
    staleTime: 300_000,
  });
  const plan = accountQuery.data?.data.account.plan;
  return plansQuery.data?.data.find((item) => item.plan === plan);
}

export function useOpenIncidents(monitorId = "") {
  return useQuery({
    queryKey: queryKeys.incidentList("open", monitorId),
    queryFn: () =>
      api<{ data: Incident[] }>(
        endpoints.incidents({ status: "open", monitor_id: monitorId || undefined }),
      ),
    refetchInterval: MONITOR_REFETCH_MS,
  });
}

export interface FleetSummary {
  total: number;
  counts: Record<MonitorState, number>;
  down: Monitor[];
}

export function summarizeFleet(monitors: Monitor[]): FleetSummary {
  const counts: Record<MonitorState, number> = { up: 0, down: 0, pending: 0, paused: 0 };
  for (const monitor of monitors) counts[monitor.state] += 1;
  return { total: monitors.length, counts, down: monitors.filter((m) => m.state === "down") };
}

export function fleetHeadline({ total, counts, down }: FleetSummary): string {
  if (total === 0) return "No monitors yet";
  if (down.length === 1) return `${down[0]!.name} is down`;
  if (down.length > 1) return `${down.length} monitors are down`;
  const active = total - counts.paused;
  if (active === 0) return "Every monitor is paused";
  if (counts.pending === active) return "Waiting for the first checks";
  return active === 1 ? "Your monitor is up" : `All ${active} monitors up`;
}

export function StatusLight({
  tone,
  className,
}: {
  tone: "up" | "down" | "idle";
  className?: string;
}) {
  return (
    <span className={cn("relative inline-flex size-2 shrink-0", className)} aria-hidden="true">
      {tone !== "idle" && (
        <span
          className={cn(
            "absolute inset-0 rounded-full motion-safe:animate-ping",
            tone === "down" ? "bg-status-down/70" : "bg-status-up/50 [animation-duration:2.4s]",
          )}
        />
      )}
      <span
        className={cn(
          "relative inline-flex size-2 rounded-full",
          tone === "down" ? "bg-status-down" : tone === "up" ? "bg-status-up" : "bg-status-idle",
        )}
      />
    </span>
  );
}

export function FleetStatus({ className }: { className?: string }) {
  const monitorsQuery = useMonitors();
  if (!monitorsQuery.data) return null;
  const fleet = summarizeFleet(monitorsQuery.data.data);
  const tone = fleet.down.length > 0 ? "down" : fleet.counts.up > 0 ? "up" : "idle";
  const to =
    fleet.down.length === 1
      ? `/app/monitors/${fleet.down[0]!.id}`
      : fleet.total === 0
        ? "/app/monitors/new"
        : "/app/monitors";
  return (
    <Link
      to={to}
      aria-live="polite"
      className={cn(
        "inline-flex h-7 min-w-0 items-center gap-2 rounded-full px-2.5 text-xs font-medium transition-colors",
        tone === "down"
          ? "bg-status-down/10 text-destructive hover:bg-status-down/15"
          : "text-muted-foreground hover:bg-muted hover:text-foreground",
        className,
      )}
    >
      <StatusLight tone={tone} />
      <span className="truncate">{fleetHeadline(fleet)}</span>
    </Link>
  );
}

export function uptimeTone(value: number | null | undefined): "good" | "fair" | "poor" | "none" {
  if (value === null || value === undefined) return "none";
  if (value >= 99.9) return "good";
  if (value >= 99) return "fair";
  return "poor";
}

const uptimeToneText = {
  good: "text-foreground",
  fair: "text-warning",
  poor: "text-destructive",
  none: "text-muted-foreground",
} as const;

const uptimeToneFill = {
  good: "bg-status-up",
  fair: "bg-status-warn",
  poor: "bg-status-down",
  none: "bg-status-idle",
} as const;

export function UptimeMeter({
  value,
  className,
}: {
  value: number | null | undefined;
  className?: string;
}) {
  const tone = uptimeTone(value);
  const width = value === null || value === undefined ? 0 : Math.max(4, (value - 90) * 10);
  return (
    <span className={cn("inline-flex items-center justify-end gap-2", className)}>
      <span className={cn("tnum text-xs", uptimeToneText[tone])}>{formatUptime(value)}</span>
      <span className="hidden h-1 w-10 overflow-hidden rounded-full bg-muted lg:inline-block">
        <span
          className={cn("block h-full rounded-full", uptimeToneFill[tone])}
          style={{ width: `${Math.min(100, width)}%` }}
        />
      </span>
    </span>
  );
}
