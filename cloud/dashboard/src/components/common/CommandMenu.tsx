import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { LayoutDashboard, LogOut, Monitor, Moon, Plus, ShieldCheck, Sun } from "lucide-react";
import { primaryNav, secondaryNav } from "@/components/layout/navigation";
import { useTheme } from "@/context/theme-context";
import { MonitorStateDot, stateLabels, useMonitors } from "@/lib/monitors";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";

export interface CommandItemDef {
  label: string;
  icon: typeof LayoutDashboard;
  path?: string;
  isCreate?: boolean;
  onSelect?: () => void;
}

const customerNavItems: CommandItemDef[] = [
  ...primaryNav.slice(0, 2).map((item) => ({ label: item.label, icon: item.icon, path: item.to })),
  { label: "Add monitor", icon: Plus, isCreate: true },
  ...primaryNav.slice(2).map((item) => ({ label: item.label, icon: item.icon, path: item.to })),
  ...secondaryNav.map((item) => ({
    label: item.label,
    icon: item.icon,
    path: item.to,
  })),
];

interface CommandMenuProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSignOut?: () => void;
  onOpenCreateMonitor?: () => void;
  workspace?: "customer" | "control";
  canAccessControl?: boolean;
  navigationItems?: CommandItemDef[];
}

export function CommandMenu({
  open,
  onOpenChange,
  onSignOut,
  onOpenCreateMonitor,
  workspace = "customer",
  canAccessControl = false,
  navigationItems,
}: CommandMenuProps) {
  const navigate = useNavigate();
  const { setTheme } = useTheme();

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === "k") {
        event.preventDefault();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, onOpenChange]);

  const monitorsQuery = useMonitors(open && workspace === "customer");
  const monitors = workspace === "customer" ? (monitorsQuery.data?.data ?? []) : [];
  const workspaceItems = navigationItems ?? customerNavItems;
  const crossWorkspace: CommandItemDef[] =
    workspace === "control"
      ? [{ label: "Customer dashboard", icon: LayoutDashboard, path: "/app" }]
      : canAccessControl
        ? [{ label: "Control plane", icon: ShieldCheck, path: "/control" }]
        : [];

  const run = (action: () => void) => {
    onOpenChange(false);
    action();
  };

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Quick navigation"
      description={
        workspace === "customer"
          ? "Run a command or jump to any page."
          : "Jump to any control-plane section."
      }
      className="max-w-xl"
    >
      <CommandInput
        placeholder={
          workspace === "customer" ? "Type a command or search…" : "Type a control command…"
        }
      />

      <CommandList className="max-h-90">
        <CommandEmpty>No matches.</CommandEmpty>

        <CommandGroup heading="Navigation">
          {[...workspaceItems, ...crossWorkspace].map((item) => (
            <CommandItem
              key={item.label}
              value={item.label}
              onSelect={() =>
                run(() => {
                  if (item.isCreate) {
                    onOpenCreateMonitor?.();
                  } else if (item.onSelect) {
                    item.onSelect();
                  } else if (item.path) {
                    void navigate(item.path);
                  }
                })
              }
            >
              <item.icon className="text-muted-foreground" />
              <span>{item.label}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        {monitors.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Monitors">
              {monitors.map((monitor) => (
                <CommandItem
                  key={monitor.id}
                  value={`monitor ${monitor.name} ${monitor.target ?? ""} ${monitor.id}`}
                  onSelect={() => run(() => navigate(`/app/monitors/${monitor.id}`))}
                >
                  <MonitorStateDot state={monitor.state} className="mx-0.5" />
                  <span className="truncate">{monitor.name}</span>
                  <span className="ml-auto text-xs text-muted-foreground">
                    {stateLabels[monitor.state]}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        <CommandSeparator />
        <CommandGroup heading="Preferences & actions">
          <CommandItem onSelect={() => run(() => setTheme("light"))}>
            <Sun className="text-muted-foreground" />
            <span>Switch to light theme</span>
          </CommandItem>
          <CommandItem onSelect={() => run(() => setTheme("dark"))}>
            <Moon className="text-muted-foreground" />
            <span>Switch to dark theme</span>
          </CommandItem>
          <CommandItem onSelect={() => run(() => setTheme("system"))}>
            <Monitor className="text-muted-foreground" />
            <span>Match system theme</span>
          </CommandItem>
          {onSignOut && (
            <CommandItem
              onSelect={() => run(onSignOut)}
              className="text-destructive data-[selected=true]:bg-destructive/10 data-[selected=true]:text-destructive *:[svg]:text-destructive"
            >
              <LogOut />
              <span>Sign out of workspace</span>
            </CommandItem>
          )}
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}
