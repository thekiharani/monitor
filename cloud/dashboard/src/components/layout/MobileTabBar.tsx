import { useState } from "react";
import { NavLink } from "react-router-dom";
import { LogOut, MoreHorizontal, Plus } from "lucide-react";
import { crossWorkspaceDestination, type NavDestination } from "@/components/layout/navigation";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";

const tabClasses =
  "flex flex-1 flex-col items-center justify-center gap-1 rounded-xl px-0.5 text-3xs font-semibold transition-colors";

function TabLink({
  destination,
  onNavigate,
}: {
  destination: NavDestination;
  onNavigate: () => void;
}) {
  const Icon = destination.icon;

  return (
    <NavLink
      to={destination.to}
      end={destination.end}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(tabClasses, isActive ? "text-primary" : "text-muted-foreground")
      }
    >
      <Icon className="size-5" />
      <span className="max-w-full truncate">{destination.label}</span>
    </NavLink>
  );
}

function MoreTile({
  destination,
  onNavigate,
}: {
  destination: NavDestination;
  onNavigate: () => void;
}) {
  const Icon = destination.icon;

  return (
    <NavLink
      to={destination.to}
      end={destination.end}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(
          "flex flex-col items-center justify-center gap-2 rounded-xl border p-4 text-center text-2xs font-semibold transition-colors",
          isActive
            ? "border-primary/40 bg-brand-soft text-primary"
            : "border-border bg-muted/30 text-foreground",
        )
      }
    >
      <Icon className="size-5" />
      <span className="text-balance">{destination.label}</span>
    </NavLink>
  );
}

const moreRowClasses =
  "flex items-center gap-3 rounded-xl px-3 py-3 text-sm font-medium text-foreground transition-colors active:bg-muted";

function MoreRow({
  destination,
  onNavigate,
}: {
  destination: NavDestination;
  onNavigate: () => void;
}) {
  const Icon = destination.icon;

  if (destination.external) {
    return (
      <a href={destination.to} onClick={onNavigate} className={moreRowClasses}>
        <Icon className="size-4 text-muted-foreground" />
        <span>{destination.label}</span>
      </a>
    );
  }

  return (
    <NavLink
      to={destination.to}
      end={destination.end}
      onClick={onNavigate}
      className={moreRowClasses}
    >
      <Icon className="size-4 text-muted-foreground" />
      <span>{destination.label}</span>
    </NavLink>
  );
}

const noSecondary: NavDestination[] = [];

const sheetCopy = {
  customer: {
    title: "Workspace",
    description: "Every other destination in this workspace.",
  },
  control: {
    title: "Control plane",
    description: "Every operator section your capabilities reach.",
  },
} as const;

export function MobileTabBar({
  workspace,
  destinations,
  tabDestinations,
  secondary = noSecondary,
  canAccessControl = false,
  onCreateMonitor,
  onSignOut,
}: {
  workspace: "customer" | "control";
  destinations: NavDestination[];
  tabDestinations: string[];
  secondary?: NavDestination[];
  canAccessControl?: boolean;
  onCreateMonitor?: () => void;
  onSignOut: () => void;
}) {
  const [moreOpen, setMoreOpen] = useState(false);
  const tabs = tabDestinations
    .map((to) => destinations.find((item) => item.to === to))
    .filter((item): item is NavDestination => Boolean(item))
    .slice(0, onCreateMonitor ? 3 : 4);
  const onTabBar = new Set(tabs.map((item) => item.to));
  const overflow = destinations.filter((item) => !onTabBar.has(item.to));
  const crossWorkspace = crossWorkspaceDestination(workspace, canAccessControl);
  const closeMore = () => setMoreOpen(false);
  const leadingTabs = onCreateMonitor ? tabs.slice(0, 2) : tabs;
  const trailingTabs = onCreateMonitor ? tabs.slice(2) : [];
  const copy = sheetCopy[workspace];

  return (
    <>
      <nav
        aria-label="Primary"
        className="fixed inset-x-0 bottom-0 z-30 border-t border-border bg-background/90 backdrop-blur-md"
      >
        <div className="flex h-16 items-stretch gap-1 px-2 pb-[env(safe-area-inset-bottom)]">
          {leadingTabs.map((destination) => (
            <TabLink key={destination.to} destination={destination} onNavigate={closeMore} />
          ))}

          {onCreateMonitor && (
            <button
              type="button"
              onClick={() => {
                closeMore();
                onCreateMonitor();
              }}
              aria-label="Add monitor"
              className="flex flex-1 flex-col items-center justify-center"
            >
              <span className="grid size-12 place-items-center rounded-xl bg-primary text-primary-foreground shadow-sm transition-transform active:scale-95">
                <Plus className="size-6" strokeWidth={2.5} />
              </span>
            </button>
          )}

          {trailingTabs.map((destination) => (
            <TabLink key={destination.to} destination={destination} onNavigate={closeMore} />
          ))}

          <button
            type="button"
            onClick={() => setMoreOpen(true)}
            aria-label="More destinations"
            className={cn(tabClasses, moreOpen ? "text-primary" : "text-muted-foreground")}
          >
            <MoreHorizontal className="size-5" />
            <span>More</span>
          </button>
        </div>
      </nav>

      <Sheet open={moreOpen} onOpenChange={setMoreOpen}>
        <SheetContent
          side="bottom"
          className="max-h-[85svh] gap-0 overflow-y-auto rounded-t-3xl p-0 pb-[env(safe-area-inset-bottom)]"
        >
          <SheetHeader className="px-5 pt-5 pb-3">
            <SheetTitle>{copy.title}</SheetTitle>
            <SheetDescription>{copy.description}</SheetDescription>
          </SheetHeader>

          {overflow.length > 0 && (
            <div className="grid grid-cols-3 gap-2.5 px-5 pb-4">
              {overflow.map((destination) => (
                <MoreTile key={destination.to} destination={destination} onNavigate={closeMore} />
              ))}
            </div>
          )}

          <Separator />

          <div className="flex flex-col gap-0.5 p-2">
            {crossWorkspace && <MoreRow destination={crossWorkspace} onNavigate={closeMore} />}

            {secondary.map((destination) => (
              <MoreRow key={destination.to} destination={destination} onNavigate={closeMore} />
            ))}

            <button
              type="button"
              onClick={() => {
                closeMore();
                onSignOut();
              }}
              className="flex items-center gap-3 rounded-xl px-3 py-3 text-left text-sm font-medium text-destructive transition-colors active:bg-destructive/10"
            >
              <LogOut className="size-4" />
              <span>Sign out</span>
            </button>
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
