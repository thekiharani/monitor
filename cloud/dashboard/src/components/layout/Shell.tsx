import { useState, type ReactNode } from "react";
import { Link as RouterLink, Navigate, useLocation, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Plus, X } from "lucide-react";
import type { Account, Usage } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { AppSidebar } from "@/components/layout/AppSidebar";
import { MobileTabBar } from "@/components/layout/MobileTabBar";
import { customerTabBar, navFor, secondaryNav } from "@/components/layout/navigation";
import { TopNavbar } from "@/components/layout/TopNavbar";
import { TimezoneProvider } from "@/context/timezone-context";
import { CommandMenu, type CommandItemDef } from "@/components/common/CommandMenu";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { useIsMobile } from "@/hooks/use-mobile";
import { useControlSession } from "@/control-api";

function useAccount() {
  return useQuery({
    queryKey: queryKeys.me,
    queryFn: () => api<{ data: { account: Account } }>(endpoints.me),
  });
}

function useUsage() {
  return useQuery({
    queryKey: queryKeys.usage,
    queryFn: () => api<{ data: Usage }>(endpoints.usage),
  });
}

function isApproachingQuota(account: Account, usage?: Usage) {
  if (!usage || account.plan !== "free") return false;
  return usage.api_requests.used / Math.max(1, usage.api_requests.limit) >= 0.8;
}

function QuotaBanner({ onDismiss }: { onDismiss: () => void }) {
  return (
    <Alert
      variant="warning"
      className="rounded-none border-x-0 border-t-0 px-4 py-2.5 pr-12 sm:px-8 sm:pr-12"
    >
      <AlertTriangle />
      <AlertTitle>You have used 80% of your free API requests.</AlertTitle>
      <AlertDescription>
        Upgrade to Starter for more monitors, faster checks and a higher API allowance.{" "}
        <RouterLink to="/app/billing" className="font-semibold">
          Upgrade now
        </RouterLink>
      </AlertDescription>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={onDismiss}
        aria-label="Dismiss quota notice"
        className="absolute top-2 right-2"
      >
        <X />
      </Button>
    </Alert>
  );
}

export function Shell({ children }: { children: ReactNode }) {
  const account = useAccount();
  const usageQuery = useUsage();
  const controlSession = useControlSession();
  const isMobile = useIsMobile();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();

  const [commandMenuOpen, setCommandMenuOpen] = useState(false);
  const [quotaBannerDismissed, setQuotaBannerDismissed] = useState(false);

  const logout = useMutation({
    mutationFn: () => api<void>(endpoints.auth.logout, { method: "POST" }),
    onSuccess: () => {
      queryClient.clear();
      void navigate("/login");
    },
  });

  if (account.isLoading) {
    return (
      <div className="grid min-h-svh place-items-center bg-background">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <Spinner className="size-6 text-primary" />
          <p className="text-xs">Loading workspace…</p>
        </div>
      </div>
    );
  }

  const user = account.data?.data.account;
  if (account.isError || !user) {
    return <Navigate to="/login" replace />;
  }

  const openCreateMonitor = () =>
    navigate("/app/monitors/new", { state: { backgroundLocation: location } });

  const commandItems: CommandItemDef[] = [
    ...navFor(user.role).map((item) => ({
      label: item.label,
      icon: item.icon,
      path: item.to,
    })),
    { label: "Add monitor", icon: Plus, isCreate: true },
    ...secondaryNav.map((item) => ({
      label: item.label,
      icon: item.icon,
      path: item.to,
    })),
  ];

  const showQuotaBanner = isApproachingQuota(user, usageQuery.data?.data) && !quotaBannerDismissed;

  return (
    <SidebarProvider>
      {!isMobile && <AppSidebar account={user} onSignOut={() => logout.mutate()} />}

      <SidebarInset className="min-w-0">
        <TopNavbar
          account={user}
          workspace="customer"
          canAccessControl={controlSession.data?.state === "admitted"}
          onOpenCommandMenu={() => setCommandMenuOpen(true)}
        />

        {showQuotaBanner && <QuotaBanner onDismiss={() => setQuotaBannerDismissed(true)} />}

        <div className="mx-auto w-full max-w-[88rem] px-4 py-6 sm:px-8 sm:py-8 lg:px-10">
          <TimezoneProvider timezone={user.timezone}>{children}</TimezoneProvider>
        </div>

        {isMobile && <div aria-hidden="true" className="h-24 shrink-0" />}
      </SidebarInset>

      {isMobile && (
        <MobileTabBar
          workspace="customer"
          destinations={navFor(user.role)}
          tabDestinations={customerTabBar}
          secondary={secondaryNav}
          canAccessControl={controlSession.data?.state === "admitted"}
          onCreateMonitor={openCreateMonitor}
          onSignOut={() => logout.mutate()}
        />
      )}

      <CommandMenu
        open={commandMenuOpen}
        onOpenChange={setCommandMenuOpen}
        workspace="customer"
        canAccessControl={controlSession.data?.state === "admitted"}
        navigationItems={commandItems}
        onSignOut={() => logout.mutate()}
        onOpenCreateMonitor={openCreateMonitor}
      />
    </SidebarProvider>
  );
}
