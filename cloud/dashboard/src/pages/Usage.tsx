import { useQuery } from "@tanstack/react-query";
import { BarChart3, Clock3, History, KeyRound, Users } from "lucide-react";
import type { Usage as UsageType } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { PageHeader } from "@/components/layout/Header";
import { UsageSkeleton } from "@/components/common/SkeletonLoaders";
import { Card, CardContent } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { cn } from "@/lib/utils";

export function Usage() {
  const usageQuery = useQuery({
    queryKey: queryKeys.usage,
    queryFn: () => api<{ data: UsageType }>(endpoints.usage),
  });

  if (usageQuery.isLoading) {
    return <UsageSkeleton />;
  }

  const usage = usageQuery.data?.data;
  if (!usage) return null;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Usage"
        description={`What this workspace has used in the ${usage.period} billing period.`}
        breadcrumbs={[{ label: "Usage" }]}
      />

      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 md:grid-cols-3">
        <UsageMeterCard
          icon={BarChart3}
          label="API Requests"
          used={usage.api_requests.used}
          limit={usage.api_requests.limit}
          unit="requests"
        />

        <UsageMeterCard
          icon={KeyRound}
          label="API Keys"
          used={usage.api_keys.used}
          limit={usage.api_keys.limit}
          unit="keys"
        />

        <UsageMeterCard
          icon={Users}
          label="Team Members"
          used={usage.members.used}
          limit={usage.members.limit}
          unit="members"
        />
      </div>

      <Card>
        <CardContent className="flex items-start gap-4 p-6">
          <span className="grid size-10 place-items-center rounded-lg bg-muted text-muted-foreground">
            <History className="size-5" />
          </span>
          <div className="space-y-1 text-xs">
            <h3 className="text-sm font-semibold text-foreground">
              {usage.retention_days} days of uptime history
            </h3>
            <p className="leading-relaxed text-muted-foreground">
              Daily uptime and response times are kept for {usage.retention_days} days on your plan.
              Individual check results are kept for 31 days, and incidents stay until you close the
              account.
            </p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="flex items-start gap-4 p-6">
          <span className="grid size-10 place-items-center rounded-xl bg-info/10 text-info">
            <Clock3 className="size-5" />
          </span>
          <div className="space-y-1 text-xs">
            <h3 className="font-semibold text-foreground text-sm">Monthly Quota Rollover</h3>
            <p className="leading-relaxed text-muted-foreground">
              Request quotas reset automatically on the 1st of each calendar month. Scheduled checks
              never count against your API allowance.
            </p>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function UsageMeterCard({
  icon: Icon,
  label,
  used,
  limit,
  unit,
}: {
  icon: typeof BarChart3;
  label: string;
  used: number;
  limit: number;
  unit: string;
}) {
  const percentage = Math.min(100, Math.round((used / Math.max(1, limit)) * 100));

  const statusColor =
    percentage >= 90 ? "text-destructive" : percentage >= 75 ? "text-warning" : "text-success";

  const progressColor =
    percentage >= 90
      ? "[&_[data-slot=progress-indicator]]:bg-destructive"
      : percentage >= 75
        ? "[&_[data-slot=progress-indicator]]:bg-warning"
        : "[&_[data-slot=progress-indicator]]:bg-primary";

  return (
    <Card className="transition-all duration-200 hover:ring-border">
      <CardContent className="p-6">
        <div className="flex items-center justify-between">
          <span className="grid size-10 place-items-center rounded-lg bg-muted text-muted-foreground">
            <Icon className="size-5" />
          </span>
          <span className={cn("text-xs font-semibold", statusColor)}>{percentage}%</span>
        </div>

        <div className="mt-5">
          <p className="text-xs font-medium text-muted-foreground">{label}</p>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="text-2xl font-semibold tracking-tight text-foreground sm:text-3xl">
              {used.toLocaleString()}
            </span>
            <span className="text-xs text-muted-foreground font-medium">
              / {limit.toLocaleString()} {unit}
            </span>
          </div>
        </div>

        <Progress value={percentage} className={cn("mt-4 h-2 bg-muted/80", progressColor)} />

        <p className="mt-3 text-2xs text-muted-foreground">
          {Math.max(0, limit - used).toLocaleString()} {unit} remaining this cycle
        </p>
      </CardContent>
    </Card>
  );
}
