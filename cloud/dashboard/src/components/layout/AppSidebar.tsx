import { NavLink, Link as RouterLink, useLocation } from "react-router-dom";
import { ArrowUpRight, ChevronsUpDown, LogOut } from "lucide-react";
import type { Account } from "@/types";
import { personInitials, personName, workspaceInitial, workspaceName } from "@/lib/identity";
import { Logo } from "@/components/layout/Logo";
import { navFor, secondaryNav, type NavDestination } from "@/components/layout/navigation";
import { useWorkspaceAccounts, WorkspaceAccountItems } from "@/components/layout/WorkspaceAccounts";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
  useSidebar,
} from "@/components/ui/sidebar";

function NavMenu({ items }: { items: NavDestination[] }) {
  const { isMobile, setOpenMobile } = useSidebar();
  const { pathname } = useLocation();

  return (
    <SidebarMenu>
      {items.map((item) => (
        <SidebarMenuItem key={item.to}>
          <SidebarMenuButton
            isActive={
              !item.external &&
              (item.end
                ? pathname === item.to
                : pathname === item.to || pathname.startsWith(`${item.to}/`))
            }
            className="h-9 rounded-md px-3 text-[13px] font-medium text-muted-foreground data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-sidebar-accent-foreground aria-[current=page]:shadow-none [&>svg]:size-4"
            tooltip={item.label}
            render={
              item.external ? (
                <a href={item.to} aria-label={item.label} />
              ) : (
                <NavLink
                  to={item.to}
                  end={item.end}
                  onClick={() => isMobile && setOpenMobile(false)}
                />
              )
            }
          >
            <item.icon />
            <span>{item.label}</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      ))}
    </SidebarMenu>
  );
}

function WorkspaceMenu() {
  const { accounts, current, switchTo, switchingTo } = useWorkspaceAccounts();
  if (accounts.length < 2) return null;

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger render={<SidebarMenuButton size="lg" tooltip="Switch workspace" />}>
            <Avatar size="sm">
              <AvatarFallback className="bg-brand-soft text-2xs font-semibold text-primary">
                {current ? workspaceInitial(current) : "?"}
              </AvatarFallback>
            </Avatar>
            <span className="min-w-0 flex-1 truncate text-left">
              {current ? workspaceName(current) : "Workspace"}
            </span>
            <ChevronsUpDown className="ml-auto text-muted-foreground" />
          </DropdownMenuTrigger>

          <DropdownMenuContent align="start" side="right" className="w-56">
            <DropdownMenuLabel>Workspaces</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <WorkspaceAccountItems
              accounts={accounts}
              switchTo={switchTo}
              switchingTo={switchingTo}
            />
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}

function UpgradeLink() {
  return (
    <RouterLink
      to="/app/billing"
      className="group flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs text-sidebar-foreground/70 transition-colors hover:bg-sidebar-accent hover:text-sidebar-foreground group-data-[collapsible=icon]:hidden"
    >
      <span>
        Free plan <span className="text-sidebar-foreground/40">·</span>{" "}
        <span className="font-medium text-primary">Upgrade</span>
      </span>
      <ArrowUpRight className="size-3.5 text-primary transition-transform group-hover:translate-x-0.5 group-hover:-translate-y-0.5" />
    </RouterLink>
  );
}

const navGroups = [
  { key: "monitoring", label: "Monitoring" },
  { key: "workspace", label: "Workspace" },
] as const;

export function AppSidebar({ account, onSignOut }: { account: Account; onSignOut: () => void }) {
  const { isMobile, setOpenMobile } = useSidebar();
  const isPaid = account.plan !== "free";

  const destinations = navFor(account.role);

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="gap-3 border-b border-sidebar-border px-3 pt-4 pb-4">
        <div className="flex h-8 items-center px-1.5 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:px-0">
          <Logo className="group-data-[collapsible=icon]:[&>span:last-child]:hidden" />
        </div>
        <WorkspaceMenu />
      </SidebarHeader>

      <SidebarContent className="gap-3 px-2 pt-4">
        {navGroups.map((group) => {
          const items = destinations.filter((item) => item.group === group.key);
          if (items.length === 0) return null;
          return (
            <SidebarGroup key={group.key} className="py-1.5">
              <SidebarGroupLabel className="px-3 text-[10px] font-medium uppercase tracking-eyebrow text-muted-foreground/80">
                {group.label}
              </SidebarGroupLabel>
              <SidebarGroupContent>
                <NavMenu items={items} />
              </SidebarGroupContent>
            </SidebarGroup>
          );
        })}
      </SidebarContent>

      <SidebarFooter className="gap-1 p-3">
        <NavMenu items={secondaryNav} />
        {!isPaid && <UpgradeLink />}
        <SidebarSeparator className="mx-0 my-1.5" />
        <SidebarMenu>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger render={<SidebarMenuButton size="lg" tooltip={account.email} />}>
                <Avatar size="sm">
                  <AvatarFallback className="bg-brand-soft text-2xs font-semibold text-primary">
                    {personInitials(account)}
                  </AvatarFallback>
                </Avatar>
                <span className="min-w-0 flex-1 text-left">
                  <span className="block truncate text-xs font-medium text-sidebar-foreground">
                    {personName(account)}
                  </span>
                  <span className="block truncate text-2xs text-muted-foreground capitalize">
                    {account.plan} plan
                  </span>
                </span>
                <ChevronsUpDown className="ml-auto text-muted-foreground" />
              </DropdownMenuTrigger>

              <DropdownMenuContent align="end" side="top" className="w-56">
                <DropdownMenuLabel className="truncate font-normal text-muted-foreground">
                  {account.email}
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => {
                    if (isMobile) setOpenMobile(false);
                    onSignOut();
                  }}
                  variant="destructive"
                >
                  <LogOut />
                  <span>Sign out</span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>

      <SidebarRail />
    </Sidebar>
  );
}
