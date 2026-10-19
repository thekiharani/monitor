import { useMemo, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { Activity, Copy, Eye, Pencil, Plus, Search } from "lucide-react";
import type { Monitor, MonitorState } from "@/types";
import { RowActions, copyRowValue, type RowActionItem } from "@/components/ui/row-actions";
import { PageHeader } from "@/components/layout/Header";
import { EmptyState } from "@/components/common/EmptyState";
import { TableSkeleton } from "@/components/common/SkeletonLoaders";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  MonitorStateDot,
  formatInterval,
  formatMs,
  formatRelative,
  formatUptime,
  kindLabels,
  UptimeMeter,
  monitorStates,
  monitorTarget,
  stateLabels,
  useMonitors,
  useNow,
} from "@/lib/monitors";

type StateFilter = MonitorState | "all";

export function Monitors() {
  const location = useLocation();
  const now = useNow();
  const monitorsQuery = useMonitors();
  const [stateFilter, setStateFilter] = useState<StateFilter>("all");
  const [search, setSearch] = useState("");

  const monitors = useMemo(() => monitorsQuery.data?.data ?? [], [monitorsQuery.data]);
  const counts = useMemo(() => {
    const tally: Record<MonitorState, number> = { up: 0, down: 0, pending: 0, paused: 0 };
    for (const monitor of monitors) tally[monitor.state] += 1;
    return tally;
  }, [monitors]);

  const visible = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return monitors.filter(
      (monitor) =>
        (stateFilter === "all" || monitor.state === stateFilter) &&
        (!needle ||
          monitor.name.toLowerCase().includes(needle) ||
          (monitor.target ?? "").toLowerCase().includes(needle)),
    );
  }, [monitors, search, stateFilter]);

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

  const actionsFor = (monitor: Monitor): RowActionItem[] => [
    { key: "view", label: "View monitor", icon: Eye, to: `/app/monitors/${monitor.id}` },
    {
      key: "edit",
      label: "Edit monitor",
      icon: Pencil,
      to: `/app/monitors/${monitor.id}/edit`,
      state: { backgroundLocation: location },
    },
    ...(monitor.target
      ? [
          {
            key: "target",
            label: "Copy target",
            icon: Copy,
            onClick: () => {
              void copyRowValue(monitor.target!, "Target");
            },
          },
        ]
      : []),
    {
      key: "copy",
      label: "Copy reference",
      icon: Copy,
      onClick: () => {
        void copyRowValue(monitor.id);
      },
    },
  ];

  return (
    <div className="space-y-4">
      <PageHeader
        title="Monitors"
        eyebrow="Monitoring"
        description="Track availability, response times, and the health of your services."
        action={monitors.length > 0 ? addButton : undefined}
      />

      {monitorsQuery.isLoading ? (
        <Card className="p-0">
          <TableSkeleton rows={5} />
        </Card>
      ) : monitors.length === 0 ? (
        <Card>
          <EmptyState
            icon={Activity}
            title="No monitors yet"
            description="Add your first monitor and we will start checking it from our probe regions within a minute."
            action={addButton}
          />
        </Card>
      ) : (
        <>
          <dl className="mb-6 grid grid-cols-2 overflow-hidden rounded-lg border bg-card sm:grid-cols-4">
            {monitorStates.map((state) => (
              <div
                key={state}
                className="border-border px-5 py-4 odd:border-r [&:nth-child(n+3)]:border-t sm:border-r sm:last:border-r-0 sm:[&:nth-child(n+3)]:border-t-0"
              >
                <dt className="flex items-center gap-2 text-xs text-muted-foreground">
                  <MonitorStateDot state={state} className="size-2" />
                  {stateLabels[state]}
                </dt>
                <dd className="tnum mt-2 text-2xl">{counts[state]}</dd>
              </div>
            ))}
          </dl>
          <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3 sm:flex-row sm:items-center sm:justify-between">
            <ToggleGroup
              aria-label="Filter by state"
              variant="outline"
              size="sm"
              className="flex-wrap"
              value={[stateFilter]}
              onValueChange={(value) => setStateFilter((value[0] as StateFilter) ?? "all")}
            >
              <ToggleGroupItem value="all" className="text-xs">
                All <span className="tabular-nums text-muted-foreground">{monitors.length}</span>
              </ToggleGroupItem>
              {monitorStates.map((state) => (
                <ToggleGroupItem key={state} value={state} className="gap-1.5 text-xs">
                  <MonitorStateDot state={state} className="size-2" />
                  {stateLabels[state]}
                  <span className="tabular-nums text-muted-foreground">{counts[state]}</span>
                </ToggleGroupItem>
              ))}
            </ToggleGroup>

            <div className="relative sm:w-72">
              <Search className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                type="search"
                aria-label="Search monitors"
                placeholder="Search by name or target"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                className="pl-8 text-xs"
              />
            </div>
          </div>

          {visible.length === 0 ? (
            <Card>
              <EmptyState
                icon={Search}
                title="No monitors match"
                description="Try a different state or search term."
              />
            </Card>
          ) : (
            <>
              <Card className="hidden overflow-hidden p-0 md:flex">
                <Table>
                  <TableHeader>
                    <TableRow className="text-xs hover:bg-transparent [&>th]:h-9 [&>th]:font-normal [&>th]:text-muted-foreground">
                      <TableHead className="pl-5">Monitor</TableHead>
                      <TableHead>Type</TableHead>
                      <TableHead>Every</TableHead>
                      <TableHead className="text-right">Response</TableHead>
                      <TableHead>Checked</TableHead>
                      <TableHead className="text-right">24h</TableHead>
                      <TableHead className="text-right">30d</TableHead>
                      <TableHead className="w-20 pr-5 text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visible.map((monitor) => (
                      <TableRow key={monitor.id} className="relative hover:bg-muted/40">
                        <TableCell className="max-w-80 py-3 pl-5">
                          <div className="flex min-w-0 items-center gap-3">
                            <MonitorStateDot state={monitor.state} />
                            <div className="min-w-0">
                              <Link
                                to={`/app/monitors/${monitor.id}`}
                                className="block truncate text-sm font-medium text-foreground after:absolute after:inset-0 hover:text-primary"
                              >
                                {monitor.name}
                              </Link>
                              <p className="truncate font-mono text-2xs text-muted-foreground">
                                {monitorTarget(monitor)}
                              </p>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {kindLabels[monitor.kind]}
                        </TableCell>
                        <TableCell className="tnum text-xs text-muted-foreground">
                          {formatInterval(monitor.interval_seconds)}
                        </TableCell>
                        <TableCell className="tnum text-right text-xs">
                          {monitor.kind === "heartbeat" ? "—" : formatMs(monitor.last_response_ms)}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatRelative(monitor.last_checked_at, now)}
                        </TableCell>
                        <TableCell className="text-right">
                          <UptimeMeter value={monitor.uptime?.["1d"]} />
                        </TableCell>
                        <TableCell className="text-right">
                          <UptimeMeter value={monitor.uptime?.["30d"]} />
                        </TableCell>
                        <TableCell className="pr-5 text-right">
                          <RowActions
                            label={`Actions for ${monitor.name}`}
                            items={actionsFor(monitor)}
                          />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Card>

              <div className="overflow-hidden rounded-xl bg-card ring-1 ring-border md:hidden">
                {visible.map((monitor) => (
                  <div
                    key={monitor.id}
                    className="relative block p-4 transition-colors before:absolute before:inset-x-4 before:top-0 before:h-px before:bg-border first:before:hidden active:bg-muted/50"
                  >
                    <Link to={`/app/monitors/${monitor.id}`} className="block pr-9">
                      <div className="flex items-center gap-3">
                        <MonitorStateDot state={monitor.state} />
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-medium text-foreground">
                            {monitor.name}
                          </p>
                          <p className="truncate font-mono text-2xs text-muted-foreground">
                            {monitorTarget(monitor)}
                          </p>
                        </div>
                        <UptimeMeter value={monitor.uptime?.["1d"]} />
                      </div>
                      <p className="mt-3 flex flex-wrap gap-x-3 gap-y-1 text-2xs text-muted-foreground">
                        <span>{kindLabels[monitor.kind]}</span>
                        <span>Every {formatInterval(monitor.interval_seconds)}</span>
                        {monitor.kind !== "heartbeat" && (
                          <span>{formatMs(monitor.last_response_ms)}</span>
                        )}
                        <span>{formatRelative(monitor.last_checked_at, now)}</span>
                        <span>30d {formatUptime(monitor.uptime?.["30d"])}</span>
                      </p>
                    </Link>
                    <div className="absolute top-3 right-3">
                      <RowActions
                        label={`Actions for ${monitor.name}`}
                        items={actionsFor(monitor)}
                      />
                    </div>
                  </div>
                ))}
              </div>
            </>
          )}
        </>
      )}
    </div>
  );
}
