import type { ReactNode } from "react";
import { Inbox, type LucideIcon } from "lucide-react";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon?: LucideIcon;
  title: string;
  description: string;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  return (
    <Empty className={cn("min-h-70 animate-in fade-in duration-300", className)}>
      <EmptyHeader>
        <EmptyMedia
          variant="icon"
          className="size-10 rounded-lg bg-muted text-muted-foreground ring-1 ring-border [&_svg:not([class*='size-'])]:size-5"
        >
          <Icon />
        </EmptyMedia>
        <EmptyTitle className="font-display text-base font-semibold">{title}</EmptyTitle>
        <EmptyDescription className="leading-relaxed">{description}</EmptyDescription>
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  );
}
