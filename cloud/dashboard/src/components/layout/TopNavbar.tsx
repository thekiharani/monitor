import { Search } from "lucide-react";
import type { Account } from "@/types";
import { Logo } from "@/components/layout/Logo";
import { AccountDropdown } from "@/components/layout/AccountDropdown";
import { Button } from "@/components/ui/button";
import { FleetStatus } from "@/lib/monitors";
import { WorkspaceSwitcher } from "@/components/layout/WorkspaceSwitcher";
import { Kbd } from "@/components/ui/kbd";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";

interface TopNavbarProps {
  account: Account;
  onOpenCommandMenu: () => void;
  workspace?: "customer" | "control";
  canAccessControl?: boolean;
}

export function TopNavbar({
  account,
  onOpenCommandMenu,
  workspace = "customer",
  canAccessControl = false,
}: TopNavbarProps) {
  const searchLabel = workspace === "control" ? "Search control commands…" : "Search or jump to…";

  return (
    <header className="sticky top-0 z-20 flex h-16 items-center gap-2 border-b border-border bg-card/95 px-4 backdrop-blur-md md:px-6">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <SidebarTrigger className="size-8 text-muted-foreground max-md:hidden" />
        <Separator orientation="vertical" className="mr-1 h-4 max-md:hidden" />
        <Logo className="min-w-0 md:hidden" />
        {workspace === "customer" && <FleetStatus className="max-md:hidden" />}
      </div>

      <div className="flex shrink-0 items-center gap-1.5 md:gap-2">
        <Button
          variant="outline"
          onClick={onOpenCommandMenu}
          aria-label={searchLabel}
          className="w-8 justify-center p-0 font-normal text-muted-foreground md:w-60 md:justify-start md:gap-2 md:px-2.5"
        >
          <Search className="shrink-0" />
          <span className="hidden truncate md:inline">{searchLabel}</span>
          <Kbd className="ml-auto hidden md:inline-flex">⌘K</Kbd>
        </Button>

        <WorkspaceSwitcher workspace={workspace} canAccessControl={canAccessControl} />

        <AccountDropdown account={account} workspace={workspace} />
      </div>
    </header>
  );
}
