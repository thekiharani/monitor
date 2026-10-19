import { useMemo, useState, type MouseEvent } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Activity,
  AlertTriangle,
  Clock,
  Gauge,
  Globe,
  Pause,
  Pencil,
  Play,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import type {
  CheckResult,
  Monitor,
  MonitorDaily,
  MonitorSeries,
  MonitorState,
  SeriesPoint,
  SeriesRange,
} from "@/types";
import { APIError, api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import {
  MONITOR_REFETCH_MS,
  MonitorStateBadge,
  formatInterval,
  formatMs,
  formatRelative,
  formatUptime,
  kindLabels,
  monitorTarget,
  stateLabels,
  useNow,
} from "@/lib/monitors";
import { MonitorIncidents } from "@/pages/Incidents";
import { CodeBlock } from "@/components/common/CodeBlock";
import { cn } from "@/lib/utils";
import { useDateFormat, useTimezone } from "@/context/timezone-context";
import { PageHeader } from "@/components/layout/Header";
import { ActionDialog } from "@/components/common/ActionDialog";
import { EmptyState } from "@/components/common/EmptyState";
import { MetricCard } from "@/components/common/MetricCard";
import { PageSkeleton, TableSkeleton } from "@/components/common/SkeletonLoaders";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const RESULTS_PAGE_SIZE = 50;
const DAY_MS = 86_400_000;
const SERIES_RANGES: SeriesRange[] = ["1h", "24h", "7d", "30d"];

const regionPalette = [
  { stroke: "stroke-chart-1", swatch: "bg-chart-1" },
  { stroke: "stroke-chart-4", swatch: "bg-chart-4" },
  { stroke: "stroke-chart-3", swatch: "bg-chart-3" },
  { stroke: "stroke-chart-5", swatch: "bg-chart-5" },
  { stroke: "stroke-info", swatch: "bg-info" },
];

const stateCardColor: Record<MonitorState, "emerald" | "rose" | "amber" | "slate"> = {
  up: "emerald",
  down: "rose",
  paused: "amber",
  pending: "slate",
};

export function MonitorDetail() {
  const location = useLocation();
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { formatDateTime } = useDateFormat();
  const now = useNow();
  const [confirmDelete, setConfirmDelete] = useState(false);

  const monitorQuery = useQuery({
    queryKey: queryKeys.monitor(id),
    queryFn: () => api<{ data: Monitor }>(endpoints.monitor(id)),
    refetchInterval: MONITOR_REFETCH_MS,
  });

  const pause = useMutation({
    mutationFn: (paused: boolean) =>
      api<{ data: Monitor }>(endpoints.monitor(id), {
        method: "PATCH",
        body: JSON.stringify({ paused }),
      }),
    onSuccess: (result) => {
      queryClient.setQueryData(queryKeys.monitor(id), result);
      void queryClient.invalidateQueries({ queryKey: queryKeys.monitors });
      toast.success(result.data.paused ? "Monitor paused." : "Monitor resumed.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't update the monitor."),
  });

  const remove = useMutation({
    mutationFn: () => api<void>(endpoints.monitor(id), { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Monitor deleted.");
      setConfirmDelete(false);
    },
    onError: (err: Error) => errorToast(err, "We couldn't delete the monitor."),
  });

  if (monitorQuery.isLoading) return <PageSkeleton />;
  const monitor = monitorQuery.data?.data;
  if (!monitor) {
    const missing = monitorQuery.error instanceof APIError && monitorQuery.error.status === 404;
    return (
      <Card>
        <EmptyState
          icon={Activity}
          title={missing ? "Monitor not found" : "We couldn't load this monitor"}
          description={
            missing
              ? "It may have been deleted, or it belongs to another workspace."
              : "Please refresh the page in a moment."
          }
          action={
            <Button variant="outline" nativeButton={false} render={<Link to="/app/monitors" />}>
              Back to monitors
            </Button>
          }
        />
      </Card>
    );
  }

  const certDaysLeft = monitor.cert_expires_at
    ? Math.floor((new Date(monitor.cert_expires_at).getTime() - now) / DAY_MS)
    : null;
  const certColor =
    certDaysLeft === null
      ? "slate"
      : certDaysLeft <= 0
        ? "rose"
        : certDaysLeft < monitor.cert_warn_days
          ? "amber"
          : "emerald";

  return (
    <div className="space-y-6">
      <div className="space-y-3">
        <PageHeader
          className="mb-0"
          title={monitor.name}
          breadcrumbs={[{ label: "Monitors", to: "/app/monitors" }, { label: monitor.name }]}
          action={
            <>
              <Button
                variant="outline"
                size="lg"
                disabled={pause.isPending}
                onClick={() => pause.mutate(!monitor.paused)}
              >
                {monitor.paused ? <Play /> : <Pause />}
                {monitor.paused ? "Resume" : "Pause"}
              </Button>
              <Button
                variant="outline"
                size="lg"
                nativeButton={false}
                render={
                  <Link
                    to={`/app/monitors/${monitor.id}/edit`}
                    state={{ backgroundLocation: location }}
                  />
                }
              >
                <Pencil />
                Edit
              </Button>
              <Button variant="destructive" size="lg" onClick={() => setConfirmDelete(true)}>
                <Trash2 />
                Delete
              </Button>
            </>
          }
        />
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <MonitorStateBadge state={monitor.state} />
          <span className="min-w-0 truncate font-mono text-foreground">
            {monitorTarget(monitor)}
          </span>
          <span aria-hidden="true">·</span>
          <span>{kindLabels[monitor.kind]}</span>
          {monitor.kind !== "heartbeat" && (
            <>
              <span aria-hidden="true">·</span>
              <span>Every {formatInterval(monitor.interval_seconds)}</span>
              <span aria-hidden="true">·</span>
              <span className="font-mono">{monitor.regions.join(", ")}</span>
            </>
          )}
        </div>
      </div>

      {monitor.state === "down" && monitor.last_error && (
        <Alert variant="destructive">
          <AlertTriangle />
          <AlertTitle>Last check failed</AlertTitle>
          <AlertDescription className="font-mono text-xs break-all">
            {monitor.last_error}
          </AlertDescription>
        </Alert>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <MetricCard
          icon={Activity}
          label="Current state"
          value={stateLabels[monitor.state]}
          detail={`Since ${formatDateTime(monitor.state_changed_at)}`}
          color={stateCardColor[monitor.state]}
        />
        {monitor.kind === "heartbeat" ? (
          <MetricCard
            icon={Gauge}
            label="Last ping"
            value={formatRelative(monitor.last_checked_at, now)}
            detail={`Expected every ${formatInterval(monitor.interval_seconds)}`}
            color="blue"
          />
        ) : (
          <MetricCard
            icon={Gauge}
            label="Last response"
            value={formatMs(monitor.last_response_ms)}
            detail={[
              monitor.last_status_code ? `HTTP ${monitor.last_status_code}` : null,
              `Checked ${formatRelative(monitor.last_checked_at, now).toLowerCase()}`,
            ]
              .filter(Boolean)
              .join(" · ")}
            color="blue"
          />
        )}
        <MetricCard
          icon={Clock}
          label="Uptime 24h"
          value={formatUptime(monitor.uptime?.["1d"])}
          detail={`7d ${formatUptime(monitor.uptime?.["7d"])} · 30d ${formatUptime(monitor.uptime?.["30d"])}`}
          color="emerald"
        />
        {monitor.kind === "heartbeat" ? (
          <MetricCard
            icon={ShieldCheck}
            label="Grace period"
            value={monitor.grace_seconds ? formatInterval(monitor.grace_seconds) : "None"}
            detail="Extra time before a late ping counts as missed"
            color="slate"
          />
        ) : (
          <MetricCard
            icon={ShieldCheck}
            label="Certificate"
            value={
              certDaysLeft === null
                ? "—"
                : certDaysLeft <= 0
                  ? "Expired"
                  : `${certDaysLeft} ${certDaysLeft === 1 ? "day" : "days"}`
            }
            detail={
              monitor.cert_expires_at
                ? `Expires ${formatDateTime(monitor.cert_expires_at)}`
                : monitor.kind === "tcp"
                  ? "Not checked for TCP monitors"
                  : "No certificate seen yet"
            }
            color={certColor}
          />
        )}
      </div>

      {monitor.kind === "heartbeat" && monitor.ping_url ? (
        <Card>
          <CardHeader className="p-6 pb-3">
            <CardTitle className="text-sm font-semibold">Ping URL</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 p-6 pt-2">
            <p className="text-xs leading-relaxed text-muted-foreground">
              Call this URL at the end of every successful run. GET and POST both work. Keep it
              private: anyone with it can report your job as healthy.
            </p>
            <CodeBlock
              title="After your job finishes"
              samples={{ curl: `curl -fsS -m 10 --retry 3 ${monitor.ping_url}` }}
            />
            <p className="text-xs text-muted-foreground">
              In a crontab:{" "}
              <code className="font-mono text-foreground">
                0 3 * * * /usr/local/bin/backup &amp;&amp; curl -fsS -m 10 --retry 3{" "}
                {monitor.ping_url}
              </code>
            </p>
          </CardContent>
        </Card>
      ) : (
        <ResponseTimeCard monitorId={monitor.id} />
      )}
      <UptimeStrip monitorId={monitor.id} now={now} />
      <MonitorIncidents monitorId={monitor.id} />
      <RecentChecks monitor={monitor} />

      <ActionDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        onOpenChangeComplete={(open) => {
          if (!open && remove.isSuccess) {
            queryClient.removeQueries({ queryKey: queryKeys.monitor(id) });
            void queryClient.invalidateQueries({ queryKey: queryKeys.monitors, exact: true });
            void navigate("/app/monitors");
          }
        }}
        title={`Delete monitor "${monitor.name}"?`}
        description="Checks stop immediately and its history is removed. This cannot be undone."
        icon={Trash2}
        tone="danger"
        targetDetails={[
          { label: "Name", value: monitor.name },
          { label: "Target", value: monitorTarget(monitor), mono: true },
        ]}
        confirmLabel="Delete monitor"
        isLoading={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </div>
  );
}

const shortTickFormat: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit" };
const longTickFormat: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
const hoverTimeFormat: Intl.DateTimeFormatOptions = {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
};

function useTimeFormatter(options: Intl.DateTimeFormatOptions) {
  const timezone = useTimezone();
  return useMemo(() => {
    try {
      return new Intl.DateTimeFormat(undefined, { ...options, timeZone: timezone || undefined });
    } catch {
      return new Intl.DateTimeFormat(undefined, options);
    }
  }, [timezone, options]);
}

function niceCeil(value: number) {
  if (value <= 0) return 100;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (step * magnitude >= value) return step * magnitude;
  }
  return 10 * magnitude;
}

interface ChartModel {
  times: number[];
  regions: string[];
  byRegion: Map<string, Map<number, SeriesPoint>>;
  failures: Map<number, number>;
  yMax: number;
}

function buildChart(points: SeriesPoint[]): ChartModel {
  const byRegion = new Map<string, Map<number, SeriesPoint>>();
  const failures = new Map<number, number>();
  const timeSet = new Set<number>();
  let peak = 0;
  for (const point of points) {
    const t = new Date(point.t).getTime();
    timeSet.add(t);
    if (!byRegion.has(point.region)) byRegion.set(point.region, new Map());
    byRegion.get(point.region)?.set(t, point);
    if (point.failures > 0) failures.set(t, (failures.get(t) ?? 0) + point.failures);
    if (point.avg_ms !== null) peak = Math.max(peak, point.avg_ms);
  }
  return {
    times: [...timeSet].toSorted((a, b) => a - b),
    regions: [...byRegion.keys()].toSorted(),
    byRegion,
    failures,
    yMax: niceCeil(peak * 1.1),
  };
}

function linePath(model: ChartModel, region: string, x: (t: number) => number) {
  const series = model.byRegion.get(region);
  let path = "";
  let open = false;
  for (const t of model.times) {
    const point = series?.get(t);
    if (!point || point.avg_ms === null) {
      open = false;
      continue;
    }
    const y = 100 - (point.avg_ms / model.yMax) * 100;
    path += `${open ? "L" : "M"}${x(t).toFixed(3)} ${y.toFixed(3)} `;
    if (!open) path += `l0.001 0 `;
    open = true;
  }
  return path.trim();
}

function ResponseTimeCard({ monitorId }: { monitorId: string }) {
  const [range, setRange] = useState<SeriesRange>("24h");
  const [hovered, setHovered] = useState<number | null>(null);
  const seriesQuery = useQuery({
    queryKey: queryKeys.monitorSeries(monitorId, range),
    queryFn: () => api<{ data: MonitorSeries }>(endpoints.monitorSeries(monitorId, range)),
    refetchInterval: MONITOR_REFETCH_MS,
  });
  const short = range === "1h" || range === "24h";
  const tickFormat = useTimeFormatter(short ? shortTickFormat : longTickFormat);
  const hoverFormat = useTimeFormatter(hoverTimeFormat);

  const model = useMemo(() => buildChart(seriesQuery.data?.data.points ?? []), [seriesQuery.data]);
  const first = model.times[0] ?? 0;
  const last = model.times.at(-1) ?? 0;
  const span = last - first;
  const x = (t: number) => (span === 0 ? 50 : ((t - first) / span) * 100);
  const bandWidth = Math.max(100 / Math.max(model.times.length, 1), 0.6);
  const hoveredTime = hovered === null ? null : model.times[hovered];

  const onMove = (event: MouseEvent<HTMLDivElement>) => {
    if (model.times.length === 0) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const fraction = (event.clientX - rect.left) / Math.max(rect.width, 1);
    const target = first + fraction * span;
    let nearest = 0;
    for (let index = 1; index < model.times.length; index += 1) {
      if (Math.abs(model.times[index]! - target) < Math.abs(model.times[nearest]! - target)) {
        nearest = index;
      }
    }
    setHovered(nearest);
  };

  return (
    <Card>
      <CardHeader className="flex flex-col gap-3 p-6 pb-2 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <CardTitle className="text-sm font-semibold">Response time</CardTitle>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Average per region. Red bands mark periods with failed checks.
          </p>
        </div>
        <ToggleGroup
          aria-label="Chart range"
          variant="outline"
          size="sm"
          spacing={0}
          value={[range]}
          onValueChange={(value) => {
            if (value[0]) {
              setRange(value[0] as SeriesRange);
              setHovered(null);
            }
          }}
        >
          {SERIES_RANGES.map((item) => (
            <ToggleGroupItem key={item} value={item} className="px-3 text-xs">
              {item}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </CardHeader>
      <CardContent className="p-6 pt-2">
        {seriesQuery.isLoading ? (
          <Skeleton className="h-52 w-full rounded-xl" />
        ) : model.times.length === 0 ? (
          <div className="grid h-52 place-items-center rounded-xl border border-dashed border-border text-xs text-muted-foreground">
            No checks in this range yet.
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex gap-2">
              <div className="relative h-52 w-12 shrink-0 text-right text-3xs tabular-nums text-muted-foreground">
                {[1, 0.5, 0].map((fraction) => (
                  <span
                    key={fraction}
                    className="absolute right-0 -translate-y-1/2"
                    style={{ top: `${(1 - fraction) * 100}%` }}
                  >
                    {Math.round(model.yMax * fraction).toLocaleString()} ms
                  </span>
                ))}
              </div>
              <div
                className="relative h-52 min-w-0 flex-1"
                onMouseMove={onMove}
                onMouseLeave={() => setHovered(null)}
              >
                <svg
                  role="img"
                  aria-label={`Average response time per region over the last ${range}`}
                  viewBox="0 0 100 100"
                  preserveAspectRatio="none"
                  className="absolute inset-0 size-full overflow-visible"
                >
                  {[0, 50, 100].map((y) => (
                    <line
                      key={y}
                      x1={0}
                      x2={100}
                      y1={y}
                      y2={y}
                      className="stroke-border"
                      strokeDasharray={y === 100 ? undefined : "2 3"}
                      vectorEffect="non-scaling-stroke"
                    />
                  ))}
                  {[...model.failures.keys()].map((t) => (
                    <rect
                      key={t}
                      x={Math.max(0, x(t) - bandWidth / 2)}
                      y={0}
                      width={bandWidth}
                      height={100}
                      className="fill-destructive/15"
                    />
                  ))}
                  {model.regions.map((region, index) => (
                    <path
                      key={region}
                      d={linePath(model, region, x)}
                      fill="none"
                      strokeWidth={2}
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      vectorEffect="non-scaling-stroke"
                      className={regionPalette[index % regionPalette.length]!.stroke}
                    />
                  ))}
                  {hoveredTime !== null && hoveredTime !== undefined && (
                    <line
                      x1={x(hoveredTime)}
                      x2={x(hoveredTime)}
                      y1={0}
                      y2={100}
                      className="stroke-foreground/30"
                      vectorEffect="non-scaling-stroke"
                    />
                  )}
                </svg>
                {hoveredTime !== null && hoveredTime !== undefined && (
                  <div
                    className={cn(
                      "pointer-events-none absolute top-2 z-10 min-w-36 rounded-lg bg-foreground px-3 py-2 text-xs text-background shadow-md",
                      x(hoveredTime) > 60 ? "-translate-x-[calc(100%+8px)]" : "translate-x-2",
                    )}
                    style={{ left: `${x(hoveredTime)}%` }}
                  >
                    <p className="font-semibold">{hoverFormat.format(hoveredTime)}</p>
                    {model.regions.map((region, index) => {
                      const point = model.byRegion.get(region)?.get(hoveredTime);
                      return (
                        <p key={region} className="mt-1 flex items-center gap-2 tabular-nums">
                          <span
                            className={cn(
                              "size-2 rounded-full",
                              regionPalette[index % regionPalette.length]!.swatch,
                            )}
                          />
                          <span className="font-mono">{region}</span>
                          <span className="ml-auto">{formatMs(point?.avg_ms)}</span>
                          {point && point.failures > 0 && (
                            <span className="text-destructive">{point.failures}✕</span>
                          )}
                        </p>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
            <div className="ml-14 flex justify-between text-3xs tabular-nums text-muted-foreground">
              <span>{tickFormat.format(first)}</span>
              {span > 0 && <span>{tickFormat.format(first + span / 2)}</span>}
              <span>{tickFormat.format(last)}</span>
            </div>
            <div className="ml-14 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              {model.regions.map((region, index) => (
                <span key={region} className="inline-flex items-center gap-1.5">
                  <span
                    className={cn(
                      "h-0.5 w-3 rounded-full",
                      regionPalette[index % regionPalette.length]!.swatch,
                    )}
                  />
                  <span className="font-mono">{region}</span>
                </span>
              ))}
              {model.failures.size > 0 && (
                <span className="inline-flex items-center gap-1.5">
                  <span className="h-2.5 w-3 rounded-sm bg-destructive/15" />
                  Failures
                </span>
              )}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function uptimeBarClass(uptime: number | null) {
  if (uptime === null) return "bg-muted";
  if (uptime >= 99.9) return "bg-success";
  if (uptime >= 99) return "bg-warning";
  return "bg-destructive";
}

function UptimeStrip({ monitorId, now }: { monitorId: string; now: number }) {
  const dailyQuery = useQuery({
    queryKey: queryKeys.monitorDaily(monitorId),
    queryFn: () => api<{ data: MonitorDaily }>(endpoints.monitorDaily(monitorId)),
    refetchInterval: 5 * 60_000,
  });
  const dayFormat = useMemo(
    () =>
      new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
        timeZone: "UTC",
      }),
    [],
  );

  const daily = dailyQuery.data?.data;
  const todayKey = new Date(now).toISOString().slice(0, 10);
  const days = useMemo(() => {
    if (!daily) return [];
    const byDay = new Map(daily.items.map((item) => [item.day, item]));
    const today = new Date(`${todayKey}T00:00:00Z`).getTime();
    return Array.from({ length: daily.days }, (_, index) => {
      const key = new Date(today - (daily.days - 1 - index) * DAY_MS).toISOString().slice(0, 10);
      return { key, item: byDay.get(key) };
    });
  }, [daily, todayKey]);

  const totals = (daily?.items ?? []).reduce(
    (sum, item) => ({ checks: sum.checks + item.checks, failures: sum.failures + item.failures }),
    { checks: 0, failures: 0 },
  );
  const overall =
    totals.checks > 0 ? ((totals.checks - totals.failures) / totals.checks) * 100 : null;

  return (
    <Card>
      <CardHeader className="flex flex-row items-end justify-between gap-3 p-6 pb-2">
        <div>
          <CardTitle className="text-sm font-semibold">
            {daily ? `${daily.days}-day uptime` : "Daily uptime"}
          </CardTitle>
          <p className="mt-0.5 text-xs text-muted-foreground">
            One bar per UTC day. Gray means no checks that day.
          </p>
        </div>
        <span className="tnum text-lg">{formatUptime(overall)}</span>
      </CardHeader>
      <CardContent className="p-6 pt-2">
        {dailyQuery.isLoading ? (
          <Skeleton className="h-10 w-full rounded-lg" />
        ) : days.length === 0 ? (
          <p className="text-xs text-muted-foreground">No daily history yet.</p>
        ) : (
          <>
            <div
              className="flex h-10 items-stretch gap-px sm:gap-0.5"
              role="list"
              aria-label="Daily uptime"
            >
              {days.map(({ key, item }) => {
                const label = `${dayFormat.format(new Date(`${key}T00:00:00Z`))}: ${
                  item && item.uptime !== null
                    ? `${formatUptime(item.uptime)} uptime, ${item.checks.toLocaleString()} checks`
                    : "no data"
                }`;
                return (
                  <Tooltip key={key}>
                    <TooltipTrigger
                      render={<span role="listitem" aria-label={label} />}
                      className={cn(
                        "min-w-0 flex-1 rounded-[2px] transition-opacity hover:opacity-70",
                        uptimeBarClass(item?.uptime ?? null),
                      )}
                    />
                    <TooltipContent>{label}</TooltipContent>
                  </Tooltip>
                );
              })}
            </div>
            <div className="mt-2 flex justify-between text-3xs text-muted-foreground">
              <span>{days.length} days ago</span>
              <span>Today</span>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function RecentChecks({ monitor }: { monitor: Monitor }) {
  const { formatDateTime } = useDateFormat();
  const [region, setRegion] = useState("");
  const resultsQuery = useInfiniteQuery({
    queryKey: queryKeys.monitorResults(monitor.id, region),
    queryFn: ({ pageParam }) =>
      api<{ data: CheckResult[] }>(
        endpoints.monitorResults(monitor.id, {
          limit: RESULTS_PAGE_SIZE,
          before: pageParam,
          region: region || undefined,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) =>
      lastPage.data.length < RESULTS_PAGE_SIZE ? undefined : lastPage.data.at(-1)?.checked_at,
    refetchInterval: MONITOR_REFETCH_MS,
  });
  const results = resultsQuery.data?.pages.flatMap((page) => page.data) ?? [];
  const isHTTP = monitor.kind !== "tcp";

  return (
    <Card className="overflow-hidden">
      <CardHeader className="flex flex-col gap-3 p-6 pb-3 sm:flex-row sm:items-center sm:justify-between">
        <CardTitle className="text-sm font-semibold">Recent checks</CardTitle>
        {monitor.regions.length > 1 && (
          <Select value={region} onValueChange={(value) => setRegion(value ?? "")}>
            <SelectTrigger
              className="w-full rounded-lg text-xs sm:w-48"
              aria-label="Filter by region"
            >
              <SelectValue>{(value: string) => value || "All regions"}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">All regions</SelectItem>
              {monitor.regions.map((item) => (
                <SelectItem key={item} value={item}>
                  {item}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </CardHeader>
      {resultsQuery.isLoading ? (
        <TableSkeleton rows={5} />
      ) : results.length === 0 ? (
        <EmptyState
          icon={Globe}
          title="No checks yet"
          description="Results appear here as soon as the first probe reports back."
          className="min-h-48"
        />
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/30 text-2xs hover:bg-muted/30">
                <TableHead className="pl-6 text-muted-foreground">Checked</TableHead>
                <TableHead className="text-muted-foreground">Region</TableHead>
                <TableHead className="text-muted-foreground">Result</TableHead>
                {isHTTP && <TableHead className="text-muted-foreground">Status</TableHead>}
                <TableHead className="text-right text-muted-foreground">Total</TableHead>
                <TableHead className="text-muted-foreground">Breakdown</TableHead>
                <TableHead className="pr-6 text-muted-foreground">Error</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {results.map((result) => (
                <TableRow key={`${result.region}-${result.checked_at}`}>
                  <TableCell className="pl-6 text-xs tabular-nums">
                    {formatDateTime(result.checked_at)}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {result.region}
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={
                        result.up
                          ? "border-success/30 bg-success/10 text-success"
                          : "border-destructive/30 bg-destructive/10 text-destructive"
                      }
                    >
                      {result.up ? "Up" : "Down"}
                    </Badge>
                  </TableCell>
                  {isHTTP && (
                    <TableCell className="text-xs tabular-nums">
                      {result.status_code ?? "—"}
                    </TableCell>
                  )}
                  <TableCell className="text-right text-xs font-medium tabular-nums">
                    {formatMs(result.total_ms)}
                  </TableCell>
                  <TableCell className="text-2xs tabular-nums text-muted-foreground">
                    {[
                      ["DNS", result.dns_ms],
                      ["Connect", result.connect_ms],
                      ["TLS", result.tls_ms],
                      ["TTFB", result.ttfb_ms],
                    ]
                      .filter(([, value]) => value !== null)
                      .map(([label, value]) => `${label} ${Math.round(Number(value))}`)
                      .join(" · ") || "—"}
                  </TableCell>
                  <TableCell
                    className="max-w-72 truncate pr-6 font-mono text-2xs text-destructive"
                    title={result.error ?? undefined}
                  >
                    {result.error ?? ""}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {resultsQuery.hasNextPage && (
            <div className="flex justify-center border-t border-border p-4">
              <Button
                variant="outline"
                size="lg"
                className="text-xs"
                disabled={resultsQuery.isFetchingNextPage}
                onClick={() => void resultsQuery.fetchNextPage()}
              >
                {resultsQuery.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
