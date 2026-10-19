import { useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, LogOut, Monitor, Moon, Sun } from "lucide-react";
import type { Account } from "@/types";
import { api, endpoints } from "@/api";
import { accountNavFor } from "@/components/layout/navigation";
import { useWorkspaceAccounts, WorkspaceAccountItems } from "@/components/layout/WorkspaceAccounts";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useTheme, type Theme } from "@/context/theme-context";
import { personInitials, personName } from "@/lib/identity";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const themeOptions: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
];

export function AccountDropdown({
  account,
  workspace = "customer",
  className,
}: {
  account: Pick<Account, "email"> & Partial<Account>;
  workspace?: "customer" | "control";
  className?: string;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { theme, setTheme } = useTheme();
  const isControl = workspace === "control";
  const workspaces = useWorkspaceAccounts();

  const logoutMutation = useMutation({
    mutationFn: () => api<void>(endpoints.auth.logout, { method: "POST" }),
    onSuccess: () => {
      queryClient.clear();
      void navigate("/login");
    },
  });

  const isPaid = Boolean(account.plan && account.plan !== "free");
  const destinations = isControl ? [] : accountNavFor(account.role);
  const showWorkspaces = !isControl && workspaces.accounts.length > 1;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="outline" size="lg" />}
        className={cn("gap-2 rounded-xl pl-1.5", className)}
      >
        <Avatar size="sm">
          <AvatarFallback className="bg-brand-soft font-bold text-primary">
            {(account.email || "?")[0]!.toUpperCase()}
          </AvatarFallback>
        </Avatar>
        <span className="hidden max-w-30 truncate font-semibold md:inline">
          {account.display_name?.trim() || (account.email || "operator").split("@")[0]}
        </span>
        <ChevronDown className="size-3 text-muted-foreground opacity-70" />
      </DropdownMenuTrigger>

      <DropdownMenuContent align="end" className="w-60">
        <div className="flex items-center gap-2.5 p-2 pb-1.5">
          <Avatar size="lg">
            <AvatarFallback className="bg-brand-soft font-bold text-primary">
              {personInitials(account)}
            </AvatarFallback>
          </Avatar>
          <div className="min-w-0 flex-1">
            <p className="truncate text-2xs font-semibold text-foreground">
              {personName(account) || "Operator"}
            </p>
            {account.display_name?.trim() && (
              <p className="truncate text-3xs text-muted-foreground">{account.email}</p>
            )}
            <div className="mt-0.5 flex items-center gap-1">
              <Badge
                variant="outline"
                className={cn(
                  "px-1.5 py-0 text-3xs font-semibold uppercase tracking-wider",
                  isPaid
                    ? "border-primary/40 bg-primary/10 text-primary"
                    : "border-border bg-muted/60 text-muted-foreground",
                )}
              >
                {isControl ? "Operator" : isPaid ? `★ ${account.plan}` : "Free"}
              </Badge>
              {!isControl && account.status && (
                <span className="text-3xs capitalize text-muted-foreground">
                  · {account.status}
                </span>
              )}
            </div>
          </div>
        </div>

        {showWorkspaces && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
            <WorkspaceAccountItems
              accounts={workspaces.accounts}
              switchTo={workspaces.switchTo}
              switchingTo={workspaces.switchingTo}
            />
          </>
        )}

        {destinations.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>Account</DropdownMenuLabel>
            {destinations.map((destination) => (
              <DropdownMenuItem
                key={destination.to}
                render={
                  destination.external ? (
                    <a href={destination.to} aria-label={destination.label} />
                  ) : undefined
                }
                onClick={destination.external ? undefined : () => navigate(destination.to)}
              >
                <destination.icon className="size-4 text-muted-foreground" />
                <span>{destination.label}</span>
              </DropdownMenuItem>
            ))}
          </>
        )}

        <DropdownMenuSeparator />
        <DropdownMenuLabel>Appearance</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={theme} onValueChange={(next) => setTheme(next as Theme)}>
          {themeOptions.map((option) => {
            const Icon = option.icon;
            return (
              <DropdownMenuRadioItem key={option.value} value={option.value}>
                <Icon className="size-4 text-muted-foreground" />
                <span>{option.label}</span>
              </DropdownMenuRadioItem>
            );
          })}
        </DropdownMenuRadioGroup>

        <DropdownMenuSeparator />

        <DropdownMenuItem variant="destructive" onClick={() => logoutMutation.mutate()}>
          <LogOut className="size-4" />
          <span>Sign out</span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
