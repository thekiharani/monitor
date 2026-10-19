import type { LucideIcon } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

type MetricColor = "cyan" | "blue" | "amber" | "emerald" | "rose" | "slate";

interface MetricCardProps {
  icon: LucideIcon;
  label: string;
  value: string | number;
  detail?: string;
  color?: MetricColor;
  percentage?: number;
  badge?: string;
  className?: string;
}

const accent: Record<MetricColor, { icon: string; bar: string }> = {
  cyan: { icon: "text-primary", bar: "bg-primary" },
  blue: { icon: "text-info", bar: "bg-info" },
  amber: { icon: "text-warning", bar: "bg-status-warn" },
  emerald: { icon: "text-success", bar: "bg-status-up" },
  rose: { icon: "text-destructive", bar: "bg-status-down" },
  slate: { icon: "text-muted-foreground", bar: "bg-muted-foreground" },
};

export function MetricCard({
  icon: Icon,
  label,
  value,
  detail,
  color = "cyan",
  percentage,
  badge,
  className,
}: MetricCardProps) {
  const styles = accent[color];

  return (
    <Card className={cn("gap-0 py-0", className)}>
      <CardContent className="p-4 sm:p-5">
        <div className="flex items-center justify-between gap-2">
          <p className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
            <Icon className={cn("size-3.5 shrink-0", styles.icon)} />
            <span className="truncate">{label}</span>
          </p>
          {badge && (
            <span className="rounded-md bg-muted px-1.5 py-0.5 text-2xs font-medium text-muted-foreground">
              {badge}
            </span>
          )}
          {percentage !== undefined && (
            <span className="tnum text-2xs text-muted-foreground">{percentage}%</span>
          )}
        </div>
        <p className="tnum mt-3 text-2xl text-card-foreground">
          {typeof value === "number" ? value.toLocaleString() : value}
        </p>
        {detail && <p className="mt-1 truncate text-xs text-muted-foreground">{detail}</p>}
        {percentage !== undefined && (
          <div className="mt-3 h-1 w-full overflow-hidden rounded-full bg-muted">
            <div
              className={cn("h-full rounded-full transition-[width] duration-500", styles.bar)}
              style={{ width: `${Math.min(100, Math.max(0, percentage))}%` }}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
