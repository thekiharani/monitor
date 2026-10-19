import {
  Activity,
  Archive,
  BarChart3,
  BellRing,
  Radio,
  RadioTower,
  BookOpen,
  CircleDollarSign,
  CreditCard,
  FileClock,
  HeartPulse,
  KeyRound,
  LayoutDashboard,
  Settings2,
  ShieldCheck,
  Siren,
  TicketCheck,
  Users,
  UsersRound,
} from "lucide-react";
import type { TeamRole } from "@/types";

export interface NavDestination {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  end?: boolean;
  external?: boolean;
  roles?: TeamRole[];
  capability?: string;
  group?: "monitoring" | "workspace";
}

export const primaryNav: NavDestination[] = [
  { to: "/app", label: "Overview", icon: LayoutDashboard, end: true, group: "monitoring" },
  { to: "/app/monitors", label: "Monitors", icon: HeartPulse, group: "monitoring" },
  { to: "/app/incidents", label: "Incidents", icon: Siren, group: "monitoring" },
  { to: "/app/alerts", label: "Alerts", icon: BellRing, group: "monitoring" },
  { to: "/app/status-pages", label: "Status pages", icon: Radio, group: "monitoring" },
  { to: "/app/team", label: "Team", icon: Users, group: "workspace" },
  { to: "/app/api-keys", label: "API Keys", icon: KeyRound, group: "workspace" },
  { to: "/app/usage", label: "Usage", icon: BarChart3, group: "workspace" },
  { to: "/app/billing", label: "Billing", icon: CreditCard, roles: ["owner"], group: "workspace" },
];

export const secondaryNav: NavDestination[] = [
  { to: "/docs", label: "API docs", icon: BookOpen, external: true },
  { to: "/app/account", label: "Account settings", icon: Settings2 },
];

export const accountNav: NavDestination[] = [
  { to: "/app/account", label: "Account settings", icon: Settings2 },
  { to: "/app/billing", label: "Billing", icon: CreditCard, roles: ["owner"] },
  { to: "/docs", label: "API docs", icon: BookOpen, external: true },
];

export const controlNav: NavDestination[] = [
  { to: "/control/overview", label: "Overview", icon: Activity, end: true },
  { to: "/control/accounts", label: "Accounts", icon: Users, capability: "platform.accounts.read" },
  {
    to: "/control/team",
    label: "Team access",
    icon: UsersRound,
    capability: "platform.members.read",
  },
  {
    to: "/control/lifecycle",
    label: "Lifecycle",
    icon: Archive,
    capability: "platform.accounts.read",
  },
  { to: "/control/usage", label: "Usage", icon: BarChart3, capability: "platform.usage.read" },
  { to: "/control/plans", label: "Plans", icon: CreditCard, capability: "platform.billing.read" },
  {
    to: "/control/billing",
    label: "Billing",
    icon: CircleDollarSign,
    capability: "platform.billing.read",
  },
  {
    to: "/control/credentials",
    label: "Credentials",
    icon: KeyRound,
    capability: "platform.credentials.read",
  },
  {
    to: "/control/probes",
    label: "Probes",
    icon: RadioTower,
    capability: "platform.probes.manage",
  },
  { to: "/control/jobs", label: "Email jobs", icon: TicketCheck, capability: "platform.jobs.read" },
  {
    to: "/control/access",
    label: "Access grants",
    icon: ShieldCheck,
    capability: "platform.access.read",
  },
  {
    to: "/control/audit",
    label: "Audit trail",
    icon: FileClock,
    capability: "platform.audit.read",
  },
];

export const customerTabBar = ["/app", "/app/monitors", "/app/incidents", "/app/alerts"];

export const controlTabBar = [
  "/control/overview",
  "/control/accounts",
  "/control/billing",
  "/control/audit",
];

export function navFor(role: TeamRole): NavDestination[] {
  return primaryNav.filter((item) => !item.roles || item.roles.includes(role));
}

export function accountNavFor(role?: TeamRole): NavDestination[] {
  return accountNav.filter(
    (item) => !item.roles || (role !== undefined && item.roles.includes(role)),
  );
}

export function controlNavFor(granted: (capability: string) => boolean): NavDestination[] {
  return controlNav.filter((item) => !item.capability || granted(item.capability));
}

export function isDestinationActive(destination: NavDestination, pathname: string): boolean {
  if (destination.end) return pathname === destination.to;
  return pathname === destination.to || pathname.startsWith(`${destination.to}/`);
}

export function crossWorkspaceDestination(
  workspace: "customer" | "control",
  canAccessControl: boolean,
): NavDestination | null {
  if (workspace === "control") {
    return { to: "/app", label: "Customer dashboard", icon: LayoutDashboard };
  }
  return canAccessControl ? { to: "/control", label: "Control plane", icon: ShieldCheck } : null;
}
