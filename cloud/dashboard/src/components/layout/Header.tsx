import type { ReactNode } from "react";
import { Breadcrumbs, type BreadcrumbItem } from "@/components/layout/Breadcrumbs";
import { cn } from "@/lib/utils";

interface PageHeaderProps {
  eyebrow?: string;
  title: string;
  description?: string;
  breadcrumbs?: BreadcrumbItem[];
  action?: ReactNode;
  className?: string;
}

export function PageHeader({
  eyebrow,
  title,
  description,
  breadcrumbs,
  action,
  className,
}: PageHeaderProps) {
  return (
    <header className={cn("mb-6 space-y-3 border-b border-border pb-6", className)}>
      {breadcrumbs && breadcrumbs.length > 1 && <Breadcrumbs items={breadcrumbs} />}

      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          {eyebrow && (
            <p className="mb-2 text-2xs font-medium uppercase tracking-eyebrow text-muted-foreground">
              {eyebrow}
            </p>
          )}
          <h1 className="font-display text-2xl font-semibold leading-tight text-foreground">
            {title}
          </h1>
          {description && (
            <p className="mt-2 max-w-2xl text-sm leading-relaxed text-muted-foreground">
              {description}
            </p>
          )}
        </div>

        {action && <div className="flex shrink-0 items-center gap-2">{action}</div>}
      </div>
    </header>
  );
}
