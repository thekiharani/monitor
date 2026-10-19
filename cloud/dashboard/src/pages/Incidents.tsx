import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { CheckCheck, Siren } from "lucide-react";
import type { Incident } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import { MONITOR_REFETCH_MS, formatDuration, useMonitors, useNow } from "@/lib/monitors";
import { useDateFormat } from "@/context/timezone-context";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/components/layout/Header";
import { EmptyState } from "@/components/common/EmptyState";
import { TableSkeleton } from "@/components/common/SkeletonLoaders";
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

function useIncidents(status: "open" | "resolved", monitorId: string, limit: number) {
  return useQuery({
    queryKey: queryKeys.incidentList(status, monitorId, limit),
    queryFn: () =>
      api<{ data: Incident[] }>(
        endpoints.incidents({ status, monitor_id: monitorId || undefined, limit }),
      ),
    refetchInterval: MONITOR_REFETCH_MS,
  });
}

function IncidentRows({ incidents, showMonitor }: { incidents: Incident[]; showMonitor: boolean }) {
  const queryClient = useQueryClient();
  const { formatDateTime } = useDateFormat();
  const now = useNow();
  const acknowledge = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.incidentAcknowledge(id), { method: "POST" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.incidents });
      toast.success("Incident acknowledged.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't acknowledge the incident."),
  });

  return (
    <ul className="divide-y divide-border">
      {incidents.map((incident) => {
        const open = incident.resolved_at === null;
        const seconds = open
          ? (now - new Date(incident.started_at).getTime()) / 1000
          : incident.duration_seconds;
        return (
          <li key={incident.id} className="flex flex-wrap items-start gap-3 px-6 py-4">
            <span
              aria-hidden="true"
              className={cn(
                "mt-1.5 size-2.5 shrink-0 rounded-full",
                open ? "bg-status-down" : "bg-status-up",
              )}
            />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                {showMonitor && (
                  <Link
                    to={`/app/monitors/${incident.monitor_id}`}
                    className="font-semibold hover:underline"
                  >
                    {incident.monitor_name}
                  </Link>
                )}
                <span
                  className={cn("text-sm", showMonitor ? "text-muted-foreground" : "font-semibold")}
                >
                  {incident.cause}
                </span>
                {open && (
                  <Badge
                    variant="outline"
                    className="border-destructive/30 bg-destructive/10 text-destructive"
                  >
                    Ongoing
                  </Badge>
                )}
                {open && incident.acknowledged_at && <Badge variant="outline">Acknowledged</Badge>}
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {formatDateTime(incident.started_at)}
                {incident.resolved_at ? ` → ${formatDateTime(incident.resolved_at)}` : ""}
                {" · "}
                {open ? "down for " : "lasted "}
                {formatDuration(seconds)}
                {incident.regions_down.length > 0 && ` · ${incident.regions_down.join(", ")}`}
              </p>
            </div>
            {open && !incident.acknowledged_at && (
              <Button
                variant="outline"
                size="sm"
                disabled={acknowledge.isPending}
                onClick={() => acknowledge.mutate(incident.id)}
              >
                <CheckCheck />
                Acknowledge
              </Button>
            )}
          </li>
        );
      })}
    </ul>
  );
}

export function MonitorIncidents({ monitorId }: { monitorId: string }) {
  const openQuery = useIncidents("open", monitorId, 5);
  const resolvedQuery = useIncidents("resolved", monitorId, 10);
  const incidents = [...(openQuery.data?.data ?? []), ...(resolvedQuery.data?.data ?? [])];
  return (
    <Card className="py-0">
      <CardHeader className="p-6 pb-3">
        <CardTitle className="text-sm font-semibold">Incidents</CardTitle>
      </CardHeader>
      <CardContent className="p-0 pb-2">
        {openQuery.isLoading || resolvedQuery.isLoading ? (
          <TableSkeleton rows={2} />
        ) : incidents.length === 0 ? (
          <p className="px-6 pb-4 text-sm text-muted-foreground">
            No incidents. A failure becomes an incident once a second check confirms it.
          </p>
        ) : (
          <IncidentRows incidents={incidents} showMonitor={false} />
        )}
      </CardContent>
    </Card>
  );
}

export function Incidents() {
  const [monitorId, setMonitorId] = useState("");
  const monitorsQuery = useMonitors();
  const openQuery = useIncidents("open", monitorId, 50);
  const resolvedQuery = useIncidents("resolved", monitorId, 100);
  const open = openQuery.data?.data ?? [];
  const resolved = resolvedQuery.data?.data ?? [];
  const monitors = monitorsQuery.data?.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Incidents"
        description="Every confirmed outage, how long it lasted and where it was seen from."
        breadcrumbs={[{ label: "Incidents" }]}
        action={
          <Select value={monitorId} onValueChange={(value) => setMonitorId(value ?? "")}>
            <SelectTrigger className="w-full rounded-lg sm:w-56" aria-label="Filter by monitor">
              <SelectValue>
                {(value: string) =>
                  monitors.find((monitor) => monitor.id === value)?.name ?? "All monitors"
                }
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">All monitors</SelectItem>
              {monitors.map((monitor) => (
                <SelectItem key={monitor.id} value={monitor.id}>
                  {monitor.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />

      <Card className="py-0">
        <CardHeader className="p-6 pb-3">
          <CardTitle className="text-sm font-semibold">
            Open {open.length > 0 && <span className="text-destructive">({open.length})</span>}
          </CardTitle>
        </CardHeader>
        <CardContent className="p-0 pb-2">
          {openQuery.isLoading ? (
            <TableSkeleton rows={2} />
          ) : open.length === 0 ? (
            <p className="px-6 pb-4 text-sm text-muted-foreground">Nothing is down right now.</p>
          ) : (
            <IncidentRows incidents={open} showMonitor />
          )}
        </CardContent>
      </Card>

      <Card className="py-0">
        <CardHeader className="p-6 pb-3">
          <CardTitle className="text-sm font-semibold">Resolved</CardTitle>
        </CardHeader>
        <CardContent className="p-0 pb-2">
          {resolvedQuery.isLoading ? (
            <TableSkeleton rows={4} />
          ) : resolved.length === 0 ? (
            <EmptyState
              icon={Siren}
              title="No incidents yet"
              description="When a monitor goes down and comes back, the outage is recorded here."
            />
          ) : (
            <IncidentRows incidents={resolved} showMonitor />
          )}
        </CardContent>
      </Card>
    </div>
  );
}
