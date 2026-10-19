import { Link, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, ChevronRight, Plus } from "lucide-react";
import type { Account, Incident, Monitor, MonitorState, Usage } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { PageHeader } from "@/components/layout/Header";
import { EmptyState } from "@/components/common/EmptyState";
import { OverviewSkeleton } from "@/components/common/SkeletonLoaders";
import { Button } from "@/components/ui/button";
import {
  MonitorStateDot,
  StatusLight,
  UptimeMeter,
  fleetHeadline,
  formatMs,
  formatRelative,
  monitorTarget,
  stateLabels,
  summarizeFleet,
  useMonitors,
  useNow,
  useOpenIncidents,
  type FleetSummary,
} from "@/lib/monitors";
import { cn } from "@/lib/utils";

const stateOrder: Record<MonitorState, number> = { down: 0, pending: 1, up: 2, paused: 3 };

const ribbonFill: Record<MonitorState, string> = {
  up: "bg-status-up",
  down: "bg-status-down",
  pending: "bg-status-idle",
  paused: "bg-status-warn/60",
};

function averageUptime(monitors: Monitor[]): number | undefined {
  const values = monitors
    .filter((monitor) => monitor.state !== "paused")
    .map((monitor) => monitor.uptime?.["1d"])
    .filter((value): value is number => value !== undefined);
  if (values.length === 0) return undefined;
  return values.reduce((sum, value) => sum + value, 0) / values.length;
}

function latestCheck(monitors: Monitor[]): string | null {
  return monitors.reduce<string | null>(
    (latest, monitor) =>
      monitor.last_checked_at && (!latest || monitor.last_checked_at > latest)
        ? monitor.last_checked_at
        : latest,
    null,
  );
}

function Verdict({
  fleet,
  monitors,
  now,
}: {
  fleet: FleetSummary;
  monitors: Monitor[];
  now: number;
}) {
  const failing = fleet.down.length > 0;
  const sorted = monitors.toSorted((a, b) => stateOrder[a.state] - stateOrder[b.state]);
  const uptime = averageUptime(monitors);
  const checked = latestCheck(monitors);

  return (
    <section
      aria-live="polite"
      className={cn(
        "overflow-hidden rounded-xl bg-card ring-1",
        failing ? "ring-status-down/40" : "ring-border",
      )}
    >
      <div className="flex flex-col gap-5 p-5 sm:flex-row sm:items-end sm:justify-between sm:p-6">
        <div className="min-w-0">
          <p className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <StatusLight tone={failing ? "down" : fleet.counts.up > 0 ? "up" : "idle"} />
            {failing ? "Outage in progress" : "Live"}
            {checked && <span>· checked {formatRelative(checked, now)}</span>}
          </p>
          <h2
            className={cn(
              "mt-2 font-display text-2xl font-semibold sm:text-3xl",
              failing && "text-destructive",
            )}
          >
            {fleetHeadline(fleet)}
          </h2>
          {failing && fleet.down[0]?.last_error && (
            <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
              {fleet.down[0]?.last_error}
            </p>
          )}
        </div>
        <dl className="flex shrink-0 gap-8">
          <div>
            <dt className="text-xs text-muted-foreground">Uptime, 24h</dt>
            <dd className="tnum mt-1 text-xl">
              {uptime === undefined ? "—" : `${uptime >= 99.995 ? "100" : uptime.toFixed(2)}%`}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">Monitors</dt>
            <dd className="tnum mt-1 text-xl">{fleet.total}</dd>
          </div>
        </dl>
      </div>

      <div className="flex h-2 gap-px bg-card px-5 pb-0 sm:px-6" aria-hidden="true">
        {sorted.map((monitor) => (
          <span
            key={monitor.id}
            title={`${monitor.name}: ${stateLabels[monitor.state]}`}
            className={cn(
              "h-full flex-1 first:rounded-l-full last:rounded-r-full",
              ribbonFill[monitor.state],
            )}
          />
        ))}
      </div>

      <dl className="mt-5 grid grid-cols-2 border-t border-border sm:grid-cols-4">
        {(["up", "down", "pending", "paused"] as MonitorState[]).map((state) => (
          <Link
            key={state}
            to="/app/monitors"
            className="flex items-center justify-between gap-3 border-border px-5 py-3 transition-colors odd:border-r hover:bg-muted/60 sm:border-r sm:px-6 sm:last:border-r-0"
          >
            <dt className="flex items-center gap-2 text-xs text-muted-foreground">
              <MonitorStateDot state={state} className="size-2 [&>span]:size-2" />
              {stateLabels[state]}
            </dt>
            <dd
              className={cn("tnum text-sm", fleet.counts[state] === 0 && "text-muted-foreground")}
            >
              {fleet.counts[state]}
            </dd>
          </Link>
        ))}
      </dl>
    </section>
  );
}

function Attention({ incidents, loading }: { incidents: Incident[]; loading: boolean }) {
  const unacknowledged = incidents.filter((incident) => !incident.acknowledged_at).length;
  return (
    <Link
      to="/app/incidents"
      className="group flex items-center gap-3 rounded-xl bg-card px-5 py-4 ring-1 ring-border transition-colors hover:bg-muted/40"
    >
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium">
          {loading
            ? "Checking incidents…"
            : incidents.length === 0
              ? "No open incidents"
              : `${incidents.length} open ${incidents.length === 1 ? "incident" : "incidents"}`}
        </span>
        <span className="block text-xs text-muted-foreground">
          {incidents.length === 0
            ? "An outage opens an incident and recovery closes it."
            : unacknowledged > 0
              ? `${unacknowledged} waiting for someone to acknowledge`
              : "Everyone has been told"}
        </span>
      </span>
      {incidents.length > 0 && unacknowledged > 0 && <StatusLight tone="down" />}
      <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
    </Link>
  );
}

function UsageLine({ usage }: { usage: Usage }) {
  const rows = [
    { label: "API requests", used: usage.api_requests.used, limit: usage.api_requests.limit },
    { label: "API keys", used: usage.api_keys.used, limit: usage.api_keys.limit },
    { label: "Team members", used: usage.members.used, limit: usage.members.limit },
  ];
  return (
    <Link
      to="/app/usage"
      className="group block rounded-xl bg-card px-5 py-4 ring-1 ring-border transition-colors hover:bg-muted/40"
    >
      <span className="flex items-center justify-between text-sm font-medium">
        This month
        <ChevronRight className="size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
      </span>
      <span className="mt-3 grid gap-2.5">
        {rows.map((row) => {
          const share = Math.min(100, (row.used / Math.max(1, row.limit)) * 100);
          return (
            <span key={row.label} className="grid gap-1">
              <span className="flex justify-between text-xs text-muted-foreground">
                {row.label}
                <span className="tnum">
                  {row.used.toLocaleString()} / {row.limit.toLocaleString()}
                </span>
              </span>
              <span className="h-1 overflow-hidden rounded-full bg-muted">
                <span
                  className={cn(
                    "block h-full rounded-full",
                    share >= 80 ? "bg-status-warn" : "bg-primary",
                  )}
                  style={{ width: `${share}%` }}
                />
              </span>
            </span>
          );
        })}
      </span>
    </Link>
  );
}

function MonitorBoard({ monitors, now }: { monitors: Monitor[]; now: number }) {
  const sorted = monitors.toSorted(
    (a, b) => stateOrder[a.state] - stateOrder[b.state] || a.name.localeCompare(b.name),
  );
  return (
    <section className="overflow-hidden rounded-xl bg-card ring-1 ring-border">
      <header className="flex items-center justify-between px-5 py-3.5">
        <h2 className="text-sm font-medium">Monitors</h2>
        <Link
          to="/app/monitors"
          className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground"
        >
          View all <ArrowRight className="size-3.5" />
        </Link>
      </header>
      <ul className="border-t border-border">
        {sorted.map((monitor) => (
          <li
            key={monitor.id}
            className="relative before:absolute before:inset-x-5 before:top-0 before:h-px before:bg-border first:before:hidden"
          >
            <Link
              to={`/app/monitors/${monitor.id}`}
              className="flex items-center gap-3 px-5 py-3 transition-colors hover:bg-muted/40"
            >
              <MonitorStateDot state={monitor.state} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{monitor.name}</span>
                <span className="block truncate font-mono text-2xs text-muted-foreground">
                  {monitor.state === "down" && monitor.last_error
                    ? monitor.last_error
                    : monitorTarget(monitor)}
                </span>
              </span>
              <span className="tnum hidden w-20 text-right text-xs text-muted-foreground sm:block">
                {monitor.kind === "heartbeat" ? "—" : formatMs(monitor.last_response_ms)}
              </span>
              <span className="hidden w-24 text-right text-xs text-muted-foreground md:block">
                {monitor.state === "down"
                  ? `down ${formatRelative(monitor.state_changed_at, now)}`
                  : formatRelative(monitor.last_checked_at, now)}
              </span>
              <UptimeMeter value={monitor.uptime?.["1d"]} className="w-16 lg:w-28" />
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Overview() {
  const location = useLocation();
  const now = useNow();
  const accountQuery = useQuery({
    queryKey: queryKeys.me,
    queryFn: () => api<{ data: { account: Account } }>(endpoints.me),
  });
  const usageQuery = useQuery({
    queryKey: queryKeys.usage,
    queryFn: () => api<{ data: Usage }>(endpoints.usage),
  });
  const monitorsQuery = useMonitors();
  const incidentsQuery = useOpenIncidents();

  if (accountQuery.isLoading || monitorsQuery.isLoading) {
    return <OverviewSkeleton />;
  }
  const account = accountQuery.data?.data.account;
  if (!account) {
    return null;
  }
  const usage = usageQuery.data?.data;
  const monitors = monitorsQuery.data?.data ?? [];
  const fleet = summarizeFleet(monitors);
  const addButton = (
    <Button
      size="xl"
      nativeButton={false}
      render={<Link to="/app/monitors/new" state={{ backgroundLocation: location }} />}
    >
      <Plus />
      Add monitor
    </Button>
  );

  return (
    <div className="space-y-4">
      <PageHeader eyebrow={account.name || undefined} title="Overview" action={addButton} />

      {monitors.length === 0 ? (
        <section className="rounded-xl bg-card ring-1 ring-border">
          <EmptyState
            title="Nothing is being watched yet"
            description="Add a URL, a TCP port or a cron heartbeat. The first check runs within a minute, from every region on your plan."
            action={addButton}
          />
        </section>
      ) : (
        <>
          <Verdict fleet={fleet} monitors={monitors} now={now} />
          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
            <MonitorBoard monitors={monitors} now={now} />
            <div className="grid content-start gap-4">
              <Attention
                incidents={incidentsQuery.data?.data ?? []}
                loading={incidentsQuery.isLoading}
              />
              {usage && <UsageLine usage={usage} />}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
