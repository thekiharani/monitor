import { Link as RouterLink } from "react-router-dom";
import { ChevronRight, Home } from "lucide-react";
import { cn } from "@/lib/utils";

export interface BreadcrumbItem {
  label: string;
  to?: string;
}

export function Breadcrumbs({ items, className }: { items: BreadcrumbItem[]; className?: string }) {
  return (
    <nav
      aria-label="Breadcrumbs"
      className={cn("flex items-center gap-1.5 text-xs text-muted-foreground", className)}
    >
      <RouterLink
        to="/app"
        className="flex items-center gap-1 transition-colors hover:text-foreground"
      >
        <Home className="size-3.5" />
        <span className="sr-only">Home</span>
      </RouterLink>

      {items.map((item, index) => {
        const isLast = index === items.length - 1;
        return (
          <div key={item.to ?? item.label} className="flex items-center gap-1.5">
            <ChevronRight className="size-3 text-muted-foreground/60" />
            {item.to && !isLast ? (
              <RouterLink to={item.to} className="transition-colors hover:text-foreground">
                {item.label}
              </RouterLink>
            ) : (
              <span className={cn("truncate font-medium", isLast && "text-foreground")}>
                {item.label}
              </span>
            )}
          </div>
        );
      })}
    </nav>
  );
}
