import { Fragment, type ElementType } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { errorToast } from "@/lib/toast";
import { MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

export type RowActionVariant = "default" | "destructive" | "warning";

export interface RowActionItem {
  key: string;
  icon?: ElementType;
  label: string;
  onClick?: () => void;
  to?: string;
  state?: unknown;
  href?: string;
  variant?: RowActionVariant;
  disabled?: boolean;
  separator?: boolean;
}

export interface RowActionsProps {
  items: RowActionItem[];
  label?: string;
  triggerClassName?: string;
}

export async function copyRowValue(value: string, label = "Reference") {
  try {
    await navigator.clipboard.writeText(value);
    toast.success(`${label} copied.`);
  } catch {
    errorToast(undefined, `We couldn't copy the ${label.toLowerCase()}.`);
  }
}

export function RowActions({ items, label = "Row actions", triggerClassName }: RowActionsProps) {
  if (items.length === 0) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon" />}
        className={cn(
          "relative z-10 size-10 rounded-lg text-muted-foreground transition-colors hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground sm:size-9",
          triggerClassName,
        )}
        aria-label={label}
      >
        <MoreHorizontal />
      </DropdownMenuTrigger>

      <DropdownMenuContent align="end" sideOffset={6} className="w-56 rounded-xl p-1.5 shadow-lg">
        {items.map((item) => {
          const Icon = item.icon;
          return (
            <Fragment key={item.key}>
              {item.separator && <DropdownMenuSeparator />}
              <DropdownMenuItem
                disabled={item.disabled}
                variant={item.variant === "destructive" ? "destructive" : "default"}
                onClick={item.onClick}
                render={
                  item.to ? (
                    <Link to={item.to} state={item.state} />
                  ) : item.href ? (
                    <a
                      href={item.href}
                      target="_blank"
                      rel="noopener noreferrer"
                      aria-label={item.label}
                    />
                  ) : undefined
                }
                className={cn(
                  "min-h-10 cursor-pointer gap-3 rounded-lg px-3 py-2 text-sm font-normal",
                  item.variant === "warning" &&
                    "text-warning focus:bg-warning/10 focus:text-warning *:[svg]:text-warning",
                )}
              >
                {Icon && (
                  <Icon
                    className={cn(
                      "size-4 shrink-0",
                      !item.variant || item.variant === "default"
                        ? "text-muted-foreground"
                        : undefined,
                    )}
                  />
                )}
                {item.label}
              </DropdownMenuItem>
            </Fragment>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
