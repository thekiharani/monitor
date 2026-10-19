import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Archive,
  Ban,
  CheckCircle2,
  ChevronRight,
  CircleDollarSign,
  Copy,
  Eye,
  Gift,
  KeyRound,
  LogOut,
  Pencil,
  RadioTower,
  Search,
  ShieldCheck,
  TicketCheck,
  UserRound,
  Users,
  UsersRound,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { formatMinor, formatUSD } from "@/lib/pricing";
import { errorToast } from "@/lib/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RowActions, copyRowValue, type RowActionItem } from "@/components/ui/row-actions";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
} from "@/components/ui/sidebar";
import type { Account, PublicPlan } from "@/types";
import { Logo } from "@/components/layout/Logo";
import { MobileTabBar } from "@/components/layout/MobileTabBar";
import { TopNavbar } from "@/components/layout/TopNavbar";
import {
  controlNavFor,
  controlTabBar,
  isDestinationActive,
  type NavDestination,
} from "@/components/layout/navigation";
import { useIsMobile } from "@/hooks/use-mobile";
import { ActionDialog } from "@/components/common/ActionDialog";
import {
  Slideover,
  SlideoverBody,
  SlideoverContent,
  SlideoverFooter,
  SlideoverHeader,
  useSlideoverState,
} from "@/components/ui/slideover";
import { formatInterval, formatRelative, useNow } from "@/lib/monitors";
import { ErrorPage } from "@/components/common/ErrorPage";
import { CommandMenu, type CommandItemDef } from "@/components/common/CommandMenu";
import { api, endpoints } from "@/api";
import {
  controlFetch,
  controlListPath,
  hasControlCapability,
  useControlSession,
  type ControlRow,
  cellText,
  cellString,
  type ControlSession,
  type ControlStats,
} from "@/control-api";
import { AccountWorkspace } from "@/pages/control/AccountWorkspace";
import { getPageMetadata, usePageMetadata } from "@/lib/metadata";
import {
  ConsoleHeader,
  ControlEmpty,
  ControlPager,
  ControlTable,
  ControlRowActions,
  Metric,
  RequireCapability,
  StatusPill,
  formatControlDate,
  useControlData,
  useControlPage,
  useDebouncedValue,
  useFilterParam,
} from "@/pages/control/shared";

function attentionTone(severity: number) {
  if (severity >= 3) return "border-destructive/30 bg-destructive/10 text-destructive";
  if (severity === 2) return "border-warning/30 bg-warning/10 text-warning";
  return "border-border bg-muted text-muted-foreground";
}

type AttentionItem = {
  count: number;
  label: string;
  detail: string;
  to: string;
  severity: number;
};

function AttentionList({ data }: { data: ControlStats }) {
  const candidates: AttentionItem[] = [
    {
      count: data.past_due_billing ?? 0,
      label: "past-due subscriptions",
      detail: "Payment failed and the grace period is running",
      to: "/control/billing",
      severity: 3,
    },
    {
      count: data.failing_jobs ?? 0,
      label: "failing email jobs",
      detail: "Retried three times or more and still undelivered",
      to: "/control/jobs",
      severity: 3,
    },
    {
      count: data.suspended_accounts ?? 0,
      label: "suspended accounts",
      detail: "Held by an operator decision",
      to: "/control/accounts",
      severity: 2,
    },
    {
      count: data.closed_pending_erasure ?? 0,
      label: "closed accounts awaiting erasure",
      detail: "Data is retained until the thirty-day window elapses",
      to: "/control/lifecycle?state=pending",
      severity: 1,
    },
    {
      count: data.pending_jobs ?? 0,
      label: "email jobs queued",
      detail: "Waiting for the next delivery pass",
      to: "/control/jobs",
      severity: 0,
    },
  ];
  const items = candidates
    .filter((item) => item.count > 0 && item.severity > 0)
    .toSorted((a, b) => b.severity - a.severity || b.count - a.count);

  if (items.length === 0) {
    return (
      <Card className="bg-success/5 ring-success/30">
        <CardContent className="flex items-center gap-3 p-5">
          <span className="grid size-10 place-items-center rounded-xl bg-success/15 text-success">
            <CheckCircle2 className="size-5" />
          </span>
          <div>
            <p className="text-sm font-semibold text-foreground">Nothing needs attention</p>
            <p className="text-xs text-muted-foreground">
              No failed payments, undelivered mail or suspended accounts.
            </p>
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <section className="space-y-3">
      <h2 className="text-xs font-medium text-muted-foreground">Needs attention</h2>
      <Card>
        <CardContent className="divide-y divide-border p-0">
          {items.map((item) => (
            <Link
              key={item.label}
              to={item.to}
              className="flex items-center gap-4 px-5 py-4 transition-colors hover:bg-muted/50"
            >
              <span
                className={cn(
                  "grid min-w-12 shrink-0 place-items-center rounded-xl border px-2 py-1 text-lg font-semibold tabular-nums",
                  attentionTone(item.severity),
                )}
              >
                {item.count.toLocaleString()}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-semibold text-foreground">{item.label}</span>
                <span className="block truncate text-xs text-muted-foreground">{item.detail}</span>
              </span>
              <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
            </Link>
          ))}
        </CardContent>
      </Card>
    </section>
  );
}

function PlanMix({ plans }: { plans: Record<string, number> }) {
  const order = ["free", "starter", "pro", "business"] as const;
  const total = order.reduce((sum, plan) => sum + (plans[plan] ?? 0), 0);
  const shades = {
    free: "bg-muted-foreground/40",
    starter: "bg-[var(--chart-2)]",
    pro: "bg-[var(--chart-1)]",
    business: "bg-[var(--chart-3)]",
  } as const;
  return (
    <Card>
      <CardContent className="space-y-3 p-5">
        <p className="text-xs font-medium text-muted-foreground">Plan distribution</p>
        <div className="flex h-2.5 overflow-hidden rounded-full bg-muted">
          {order.map((plan) =>
            (plans[plan] ?? 0) > 0 ? (
              <span
                key={plan}
                className={shades[plan]}
                style={{ width: `${((plans[plan] ?? 0) / Math.max(1, total)) * 100}%` }}
              />
            ) : null,
          )}
        </div>
        <div className="flex flex-wrap gap-x-5 gap-y-1.5">
          {order.map((plan) => (
            <span key={plan} className="inline-flex items-center gap-1.5 text-xs">
              <span className={cn("size-2 rounded-full", shades[plan])} />
              <span className="capitalize text-muted-foreground">{plan}</span>
              <span className="font-semibold tabular-nums text-foreground">{plans[plan] ?? 0}</span>
            </span>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function RuntimeHealth({ session }: { session: ControlSession }) {
  const allowed = hasControlCapability(session, "platform.health.read");
  const query = useQuery({
    queryKey: ["control", "health"],
    queryFn: () =>
      controlFetch<{ data: Record<string, number> }>("/api/control/v1/health").then(
        (body) => body.data,
      ),
    enabled: allowed,
    refetchInterval: 15000,
  });
  if (!allowed) return null;
  const data = query.data ?? {};
  const paystackFailures = Number(data.paystack_failures ?? 0);
  const emailFailures = Number(data.ses_failures ?? 0);
  const acquired = Number(data.db_pool_acquired ?? 0);
  const poolMax = Number(data.db_pool_max ?? 1);
  const poolStrained = acquired >= poolMax * 0.8;

  const readings: Array<{ label: string; value: string; warn?: boolean }> = [
    { label: "HTTP requests", value: Number(data.http_requests ?? 0).toLocaleString() },
    { label: "Email queue", value: Number(data.email_queue_depth ?? 0).toLocaleString() },
    {
      label: "Paystack failures",
      value: paystackFailures.toLocaleString(),
      warn: paystackFailures > 0,
    },
    { label: "Email failures", value: emailFailures.toLocaleString(), warn: emailFailures > 0 },
    { label: "DB pool", value: `${acquired} / ${poolMax}`, warn: poolStrained },
    { label: "DB latency", value: `${data.db_latency_ms ?? 0} ms` },
    { label: "Rate limited", value: Number(data.rate_limit_rejected ?? 0).toLocaleString() },
    { label: "Quota rejected", value: Number(data.quota_rejected ?? 0).toLocaleString() },
  ];

  return (
    <section className="space-y-3">
      <h2 className="text-xs font-medium text-muted-foreground">This instance</h2>
      <Card>
        <CardContent className="p-5">
          <dl className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
            {readings.map((reading) => (
              <div key={reading.label}>
                <dt className="text-2xs text-muted-foreground">{reading.label}</dt>
                <dd
                  className={cn(
                    "mt-0.5 text-sm font-semibold tabular-nums",
                    reading.warn ? "text-warning" : "text-foreground",
                  )}
                >
                  {reading.value}
                </dd>
              </div>
            ))}
          </dl>
          <p className="mt-4 text-2xs text-muted-foreground">
            Counters belong to the instance answering this request and reset when it restarts. With
            more than one instance running, refresh to sample another.
          </p>
        </CardContent>
      </Card>
    </section>
  );
}

function Overview({ session }: { session: ControlSession }) {
  const stats = useQuery({
    queryKey: ["control", "stats"],
    queryFn: () =>
      controlFetch<{ data: ControlStats }>("/api/control/v1/stats").then((body) => body.data),
  });
  const accounts = useControlData(
    "/api/control/v1/accounts?limit=8",
    hasControlCapability(session, "platform.accounts.read"),
  );
  const data = stats.data ?? {};
  return (
    <div className="space-y-8">
      <ConsoleHeader
        title="Operations overview"
        description="Platform totals and the tenants that need attention."
        onRefresh={() => {
          void stats.refetch();
          void accounts.refetch();
        }}
        isFetching={stats.isFetching}
      />
      <AttentionList data={data} />

      {data.mrr_minor !== undefined && (
        <section className="space-y-3">
          <h2 className="text-xs font-medium text-muted-foreground">Business</h2>
          <div className="grid grid-cols-[minmax(0,1fr)] gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <Metric
              label="Monthly recurring revenue"
              value={formatMinor("KES", data.mrr_minor ?? 0)}
              icon={CircleDollarSign}
              tone="success"
            />
            <Metric
              label="Paying accounts"
              value={data.paying_accounts ?? "—"}
              icon={Users}
              tone="success"
            />
            <Metric
              label="Complimentary accounts"
              value={data.complimentary_accounts ?? "—"}
              icon={Gift}
            />
            <Metric
              label="New accounts (30d)"
              value={data.accounts_new_30d ?? "—"}
              icon={Activity}
            />
            <Metric
              label="Closed accounts (30d)"
              value={data.accounts_closed_30d ?? "—"}
              icon={Archive}
            />
          </div>
          {data.plans && <PlanMix plans={data.plans} />}
        </section>
      )}

      <section className="space-y-3">
        <h2 className="text-xs font-medium text-muted-foreground">Platform</h2>
        <div className="grid grid-cols-[minmax(0,1fr)] gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <Metric
            label="Customer accounts"
            value={data.accounts ?? "—"}
            icon={Users}
            to="/control/accounts"
          />
          <Metric
            label="Active accounts"
            value={data.active_accounts ?? "—"}
            icon={Activity}
            to="/control/accounts"
          />
          <Metric
            label="Team members"
            value={data.team_members ?? "—"}
            icon={UsersRound}
            to="/control/team"
          />
          <Metric
            label="Active credentials"
            value={data.active_credentials ?? "—"}
            icon={KeyRound}
            to="/control/credentials"
          />
        </div>
      </section>

      <RuntimeHealth session={session} />
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent accounts</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {(accounts.data ?? []).map((row) => (
            <Link
              key={cellString(row.id)}
              to={`/control/accounts/${cellText(row.id)}`}
              className="flex w-full items-center gap-3 rounded-xl p-3 text-left transition-colors hover:bg-muted"
            >
              <span className="grid size-9 place-items-center rounded-lg bg-muted text-muted-foreground">
                <UserRound className="size-4" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">
                  {cellText(row.email, "Unverified principal")}
                </span>
                <span className="block text-2xs text-muted-foreground">{cellText(row.id)}</span>
              </span>
              <StatusPill value={cellText(row.status)} />
              <ChevronRight className="size-4 text-muted-foreground" />
            </Link>
          ))}
          {!accounts.data?.length && <ControlEmpty label="No accounts available" />}
        </CardContent>
      </Card>
    </div>
  );
}

function Accounts({ session }: { session: ControlSession }) {
  const [search, setSearch] = useState("");
  const debounced = useDebouncedValue(search);
  const query = useControlPage("/api/control/v1/accounts", { q: debounced });
  const rows = query.rows;

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Accounts"
        description="Open a tenant workspace, or change lifecycle state with an auditable reason."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <div className="relative max-w-md">
        <Search className="absolute left-3 top-2.5 size-4 text-muted-foreground" />
        <Input
          className="rounded-lg pl-9"
          placeholder="Search email or account ID"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
      </div>
      {query.isLoading ? (
        <ControlEmpty label="Loading accounts…" />
      ) : rows.length ? (
        <>
          <ControlTable headers={["Account", "Plan", "Status", "Created", "Actions"]}>
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                <TableCell className="px-4 py-3">
                  <Link
                    to={`/control/accounts/${cellText(row.id)}`}
                    className="font-semibold hover:text-primary"
                  >
                    {cellText(row.email, "Unverified")}
                  </Link>
                  <p className="mt-1 font-mono text-3xs text-muted-foreground">
                    {cellText(row.id)}
                  </p>
                  {row.principal_id && (
                    <p className="font-mono text-3xs text-muted-foreground">
                      principal {cellText(row.principal_id)}
                    </p>
                  )}
                </TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={cellText(row.plan)} />
                </TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={cellText(row.status)} />
                </TableCell>
                <TableCell className="px-4 py-3 text-muted-foreground">
                  {formatControlDate(cellString(row.created_at))}
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <RowActions
                    label={`Actions for ${cellText(row.email, cellString(row.id))}`}
                    items={[
                      {
                        key: "view",
                        label: "View account",
                        icon: UserRound,
                        to: `/control/accounts/${cellString(row.id)}`,
                      },
                      ...(hasControlCapability(session, "platform.billing.comp")
                        ? [
                            {
                              key: "plan",
                              label: "Manage plan",
                              icon: CircleDollarSign,
                              to: `/control/accounts/${cellString(row.id)}#plan`,
                            },
                          ]
                        : []),
                      {
                        key: "copy",
                        label: "Copy reference",
                        icon: Copy,
                        onClick: () => {
                          void copyRowValue(cellString(row.id));
                        },
                      },
                    ]}
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
    </div>
  );
}

function Usage() {
  const [accountId, setAccountId] = useState("");
  const debounced = useDebouncedValue(accountId);
  const filter = debounced.length === 36 ? debounced : "";
  const query = useControlPage("/api/control/v1/usage", { account_id: filter });
  const rows = query.rows;
  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Usage"
        description="Monthly API request counters by account."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <Input
        className="max-w-md rounded-lg"
        placeholder="Filter by account ID"
        value={accountId}
        onChange={(event) => setAccountId(event.target.value)}
      />
      {rows.length ? (
        <>
          <ControlTable headers={["Account", "Period", "API requests", "Actions"]}>
            {rows.map((row) => (
              <TableRow key={`${cellText(row.account_id)}-${cellText(row.period)}`}>
                <TableCell className="px-4 py-3">
                  <Link
                    to={`/control/accounts/${cellText(row.account_id)}`}
                    className="font-mono text-3xs text-primary hover:underline"
                  >
                    {cellText(row.account_id)}
                  </Link>
                </TableCell>
                <TableCell className="px-4 py-3">{cellText(row.period)}</TableCell>
                <TableCell className="px-4 py-3">{cellText(row.api_requests)}</TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions
                    row={row}
                    label={`Usage ${cellString(row.account_id)} ${cellString(row.period)}`}
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
    </div>
  );
}

type ControlPlan = PublicPlan & {
  version: number;
  kes_plan_code: string;
  usd_plan_code: string;
  requests_per_second: number;
  burst: number;
};

const planLimitFields: Array<{
  key: keyof Pick<
    ControlPlan,
    | "monitors"
    | "min_interval_seconds"
    | "regions"
    | "status_pages"
    | "custom_domains"
    | "alert_channels"
    | "retention_days"
    | "api_keys"
    | "monthly_api_requests"
    | "team_members"
    | "requests_per_second"
    | "burst"
  >;
  label: string;
  min: number;
  max: number;
  step?: number;
}> = [
  { key: "monitors", label: "Monitors", min: 1, max: 1000000 },
  { key: "min_interval_seconds", label: "Minimum check interval (seconds)", min: 30, max: 86400 },
  { key: "regions", label: "Regions per monitor", min: 1, max: 16 },
  { key: "status_pages", label: "Status pages", min: 1, max: 10000 },
  { key: "custom_domains", label: "Custom domains", min: 0, max: 10000 },
  { key: "alert_channels", label: "Alert channels", min: 1, max: 10000 },
  { key: "retention_days", label: "History retention (days)", min: 1, max: 3650 },
  { key: "api_keys", label: "API keys", min: 1, max: 10000 },
  { key: "monthly_api_requests", label: "Monthly API requests", min: 1, max: 1000000000000 },
  { key: "team_members", label: "Team members", min: 1, max: 10000 },
  {
    key: "requests_per_second",
    label: "API requests per second",
    min: 0.1,
    max: 100000,
    step: 0.1,
  },
  { key: "burst", label: "API burst allowance", min: 1, max: 1000000 },
];

export function Plans({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [draft, setDraft, draftOpen, draftComplete] = useSlideoverState<ControlPlan | null>(null);
  const [price, setPrice] = useState("");
  const [editing, setEditing] = useState(false);
  const [reason, setReason] = useState("");
  const [search, setSearch] = useState("");
  const debounced = useDebouncedValue(search);
  const canManage = hasControlCapability(session, "platform.plans.manage");
  const canReadAccounts = hasControlCapability(session, "platform.accounts.read");
  const canAssign = hasControlCapability(session, "platform.billing.comp");
  const plans = useQuery({
    queryKey: ["control", "plans"],
    queryFn: () =>
      controlFetch<{ data: ControlPlan[] }>("/api/control/v1/plans").then((body) => body.data),
  });
  const accounts = useControlPage(
    "/api/control/v1/accounts",
    { q: debounced },
    canReadAccounts,
    "plan-accounts",
  );
  const openPlan = (plan: ControlPlan, edit: boolean) => {
    setDraft({ ...plan });
    setPrice(String(plan.price_minor / 100));
    setReason("");
    setEditing(edit);
  };
  const save = useMutation({
    mutationFn: (auditReason: string) => {
      if (!draft) throw new Error("Choose a plan to edit.");
      return controlFetch(`/api/control/v1/plans/${draft.plan}`, {
        method: "PATCH",
        body: JSON.stringify({
          ...draft,
          price_minor: Math.round(Number(price) * 100),
          reason: auditReason,
        }),
      });
    },
    onSuccess: () => {
      toast.success("Plan updated");
      setDraft(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
      void queryClient.invalidateQueries({ queryKey: ["plans"] });
      void queryClient.invalidateQueries({ queryKey: ["usage"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't update this plan."),
  });
  return (
    <div className="space-y-8">
      <ConsoleHeader
        title="Plans"
        description="Manage plan pricing, limits and customer assignments."
        onRefresh={() => {
          void plans.refetch();
          if (canReadAccounts) void accounts.refetch();
        }}
        isFetching={plans.isFetching}
      />
      {plans.isError ? (
        <ControlEmpty label="We couldn't load the plan catalog. Try refreshing." />
      ) : plans.isLoading ? (
        <ControlEmpty label="Loading plans…" />
      ) : (
        <div className="overflow-hidden rounded-lg border bg-card">
          <Table aria-label="Plan catalog" className="min-w-[54rem]">
            <TableHeader>
              <TableRow className="bg-muted/30 hover:bg-muted/30">
                {[
                  "Plan",
                  "Monthly price",
                  "Monitors",
                  "Check interval",
                  "API requests / month",
                  "Team seats",
                  "Actions",
                ].map((label) => (
                  <TableHead
                    key={label}
                    className={cn(
                      "h-11 px-5 text-xs text-muted-foreground",
                      label === "Actions" && "w-20 text-right",
                    )}
                  >
                    {label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {plans.data?.map((plan) => (
                <TableRow key={plan.plan}>
                  <TableCell className="px-5 py-4">
                    <p className="font-semibold capitalize">{plan.plan}</p>
                    <p className="mt-1 text-xs text-muted-foreground">Version {plan.version}</p>
                  </TableCell>
                  <TableCell className="px-5 py-4">
                    <p className="font-medium tabular-nums">
                      {formatMinor("KES", plan.price_minor)}
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {formatUSD(plan.price_minor)} / month
                    </p>
                  </TableCell>
                  <TableCell className="px-5 tabular-nums">
                    {plan.monitors.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-5">
                    {formatInterval(plan.min_interval_seconds)}
                  </TableCell>
                  <TableCell className="px-5 tabular-nums">
                    {plan.monthly_api_requests.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-5 tabular-nums">
                    {plan.team_members.toLocaleString()}
                  </TableCell>
                  <TableCell className="px-5">
                    <div className="flex justify-end">
                      <RowActions
                        label={`Actions for ${plan.plan} plan`}
                        items={[
                          {
                            key: "view",
                            label: "View plan",
                            icon: Eye,
                            onClick: () => openPlan(plan, false),
                          },
                          ...(canManage
                            ? [
                                {
                                  key: "edit",
                                  label: "Edit plan",
                                  icon: Pencil,
                                  onClick: () => openPlan(plan, true),
                                },
                              ]
                            : []),
                          {
                            key: "copy",
                            label: "Copy reference",
                            icon: Copy,
                            onClick: () => {
                              void copyRowValue(plan.plan);
                            },
                          },
                        ]}
                      />
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {plans.data?.length === 0 && <ControlEmpty label="No plans in the catalog" />}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        Prices apply to new purchases. Existing subscriptions keep their billing terms. Limit
        changes apply to accounts on that plan; other app servers refresh within 30 seconds.
      </p>
      {canReadAccounts && (
        <section className="space-y-4">
          <div>
            <h2 className="text-lg font-semibold">Account plans</h2>
            <p className="text-xs text-muted-foreground">
              Open an account to grant, change or end a complimentary plan. Paid subscriptions are
              managed by the account owner.
            </p>
          </div>
          <Input
            aria-label="Search accounts for plan management"
            className="max-w-md"
            placeholder="Search email or account ID"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
          {accounts.isLoading ? (
            <ControlEmpty label="Loading accounts…" />
          ) : accounts.rows.length ? (
            <>
              <ControlTable headers={["Account", "Current plan", "Status", "Actions"]}>
                {accounts.rows.map((row) => (
                  <TableRow key={cellString(row.id)}>
                    <TableCell className="px-4 py-3">
                      <Link
                        to={`/control/accounts/${cellString(row.id)}`}
                        className="font-medium hover:text-primary"
                      >
                        {cellText(row.email, "Unverified")}
                      </Link>
                      <p className="font-mono text-3xs text-muted-foreground">
                        {cellString(row.id)}
                      </p>
                    </TableCell>
                    <TableCell className="px-4 py-3">
                      <StatusPill value={cellText(row.plan)} />
                    </TableCell>
                    <TableCell className="px-4 py-3">
                      <StatusPill value={cellText(row.status)} />
                    </TableCell>
                    <TableCell className="px-4 py-3 text-right">
                      <RowActions
                        label={`Actions for account ${cellText(row.email, cellString(row.id))}`}
                        items={[
                          {
                            key: "view",
                            label: "View account",
                            icon: UserRound,
                            to: `/control/accounts/${cellString(row.id)}`,
                          },
                          ...(canAssign
                            ? [
                                {
                                  key: "plan",
                                  label: "Manage plan",
                                  icon: CircleDollarSign,
                                  to: `/control/accounts/${cellString(row.id)}#plan`,
                                },
                              ]
                            : []),
                          {
                            key: "copy",
                            label: "Copy reference",
                            icon: Copy,
                            onClick: () => {
                              void copyRowValue(cellString(row.id));
                            },
                          },
                        ]}
                      />
                    </TableCell>
                  </TableRow>
                ))}
              </ControlTable>
              <ControlPager query={accounts} />
            </>
          ) : (
            <ControlEmpty
              label={accounts.isError ? "We couldn't load accounts." : "No matching accounts"}
            />
          )}
        </section>
      )}
      {draft && (
        <Slideover
          open={draftOpen}
          onOpenChangeComplete={draftComplete}
          onOpenChange={(open) => {
            if (!open && !save.isPending) setDraft(null);
          }}
        >
          <SlideoverContent size={editing ? "3xl" : "2xl"}>
            <form
              className="flex min-h-0 flex-1 flex-col"
              onSubmit={(event) => {
                event.preventDefault();
                if (editing && canManage && !save.isPending && reason.trim().length >= 3)
                  save.mutate(reason.trim());
              }}
            >
              <SlideoverHeader
                title={
                  editing ? (
                    `Edit ${draft.plan} plan`
                  ) : (
                    <span className="capitalize">{draft.plan} plan</span>
                  )
                }
                description={
                  editing
                    ? "Update pricing, service limits, and billing configuration. Changes are recorded in the audit log."
                    : "Catalog pricing, included allowances, and billing configuration."
                }
                icon={CircleDollarSign}
                showCloseButton={!save.isPending}
              />
              <SlideoverBody key={editing ? "editor" : "details"} className="space-y-8">
                {editing ? (
                  <>
                    <section className="space-y-5">
                      <div>
                        <h3 className="text-sm font-semibold">Monthly pricing</h3>
                        <p className="mt-1 text-sm text-muted-foreground">
                          New purchases use this price. Existing subscriptions keep their billing
                          terms.
                        </p>
                      </div>
                      <div className="grid gap-5 sm:grid-cols-2">
                        <div className="space-y-2">
                          <Label htmlFor="plan-price">Monthly price (Ksh)</Label>
                          <Input
                            id="plan-price"
                            type="number"
                            min={draft.plan === "free" ? 0 : 1}
                            max={1000000000}
                            step="0.01"
                            required
                            disabled={draft.plan === "free" || save.isPending}
                            value={price}
                            onChange={(event) => setPrice(event.target.value)}
                          />
                        </div>
                        <div className="space-y-2">
                          <p className="text-sm leading-none font-medium">Monthly price ($)</p>
                          <p className="flex h-11 items-center rounded-lg border border-dashed bg-muted/30 px-3.5 text-sm tabular-nums">
                            {formatUSD(Math.round(Number(price) * 100))}
                          </p>
                          <p className="text-xs text-muted-foreground">
                            Calculated from Ksh at 100 Ksh per USD.
                          </p>
                        </div>
                      </div>
                    </section>
                    <section className="space-y-5 border-t pt-6">
                      <div>
                        <h3 className="text-sm font-semibold">Service limits</h3>
                        <p className="mt-1 text-sm text-muted-foreground">
                          Applies to accounts on this plan. Other app servers refresh within 30
                          seconds.
                        </p>
                      </div>
                      <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2">
                        {planLimitFields.map((field) => (
                          <div key={field.key} className="space-y-2">
                            <Label htmlFor={`plan-${field.key}`}>{field.label}</Label>
                            <Input
                              id={`plan-${field.key}`}
                              type="number"
                              required
                              disabled={save.isPending}
                              min={field.min}
                              max={field.max}
                              step={field.step ?? 1}
                              value={draft[field.key]}
                              onChange={(event) =>
                                setDraft({ ...draft, [field.key]: Number(event.target.value) })
                              }
                            />
                          </div>
                        ))}
                      </div>
                    </section>
                    {draft.plan !== "free" && (
                      <section className="space-y-5 border-t pt-6">
                        <div>
                          <h3 className="text-sm font-semibold">Paystack monthly plans</h3>
                          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
                            When changing a live price, create matching monthly plans in Paystack
                            and enter their new codes. Existing subscriptions keep the old codes.
                          </p>
                        </div>
                        <div className="grid gap-5 sm:grid-cols-2">
                          <div className="space-y-2">
                            <Label htmlFor="plan-kes-code">Ksh plan code</Label>
                            <Input
                              id="plan-kes-code"
                              disabled={save.isPending}
                              value={draft.kes_plan_code}
                              onChange={(event) =>
                                setDraft({ ...draft, kes_plan_code: event.target.value })
                              }
                            />
                          </div>
                          <div className="space-y-2">
                            <Label htmlFor="plan-usd-code">USD ($) plan code</Label>
                            <Input
                              id="plan-usd-code"
                              disabled={save.isPending}
                              value={draft.usd_plan_code}
                              onChange={(event) =>
                                setDraft({ ...draft, usd_plan_code: event.target.value })
                              }
                            />
                          </div>
                        </div>
                      </section>
                    )}
                    <section className="space-y-2 border-t pt-6">
                      <Label htmlFor="plan-audit-reason">Auditable Reason</Label>
                      <Input
                        id="plan-audit-reason"
                        required
                        minLength={3}
                        disabled={save.isPending}
                        placeholder="Why are you changing this plan?"
                        value={reason}
                        onChange={(event) => setReason(event.target.value)}
                      />
                      <p className="text-xs text-muted-foreground">
                        Recorded in the audit log with your operator identity.
                      </p>
                    </section>
                  </>
                ) : (
                  <>
                    <dl className="grid gap-4 sm:grid-cols-2">
                      <div className="rounded-lg border bg-muted/20 p-5">
                        <dt className="text-xs text-muted-foreground">Monthly price · KES</dt>
                        <dd className="mt-2 text-2xl font-semibold tabular-nums">
                          {formatMinor("KES", draft.price_minor)}
                        </dd>
                      </div>
                      <div className="rounded-lg border bg-muted/20 p-5">
                        <dt className="text-xs text-muted-foreground">Monthly price · USD</dt>
                        <dd className="mt-2 text-2xl font-semibold tabular-nums">
                          {formatUSD(draft.price_minor)}
                        </dd>
                      </div>
                    </dl>
                    <section className="space-y-4">
                      <h3 className="text-sm font-semibold">Plan limits</h3>
                      <div className="overflow-hidden rounded-lg border">
                        <Table aria-label="Plan limits">
                          <TableHeader>
                            <TableRow className="bg-muted/30">
                              <TableHead className="px-4">Allowance</TableHead>
                              <TableHead className="px-4 text-right">Included</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {planLimitFields.map((field) => (
                              <TableRow key={field.key}>
                                <TableCell className="whitespace-normal px-4 py-3 text-muted-foreground">
                                  {field.label}
                                </TableCell>
                                <TableCell className="px-4 text-right font-medium tabular-nums">
                                  {draft[field.key].toLocaleString()}
                                </TableCell>
                              </TableRow>
                            ))}
                          </TableBody>
                        </Table>
                      </div>
                    </section>
                    <section className="space-y-4">
                      <h3 className="text-sm font-semibold">Billing configuration</h3>
                      <dl className="divide-y rounded-lg border px-4 text-sm">
                        <div className="flex justify-between gap-4 py-3">
                          <dt className="text-muted-foreground">Catalog version</dt>
                          <dd className="tabular-nums">{draft.version}</dd>
                        </div>
                        {draft.plan !== "free" && (
                          <>
                            <div className="flex flex-wrap justify-between gap-2 py-3">
                              <dt className="text-muted-foreground">Ksh Paystack plan</dt>
                              <dd className="break-all font-mono text-xs">
                                {draft.kes_plan_code || "Not configured"}
                              </dd>
                            </div>
                            <div className="flex flex-wrap justify-between gap-2 py-3">
                              <dt className="text-muted-foreground">USD Paystack plan</dt>
                              <dd className="break-all font-mono text-xs">
                                {draft.usd_plan_code || "Not configured"}
                              </dd>
                            </div>
                          </>
                        )}
                      </dl>
                    </section>
                  </>
                )}
              </SlideoverBody>
              <SlideoverFooter>
                <Button
                  type="button"
                  variant="outline"
                  disabled={save.isPending}
                  onClick={() => setDraft(null)}
                >
                  {editing ? "Cancel" : "Done"}
                </Button>
                {editing ? (
                  <Button type="submit" disabled={save.isPending || reason.trim().length < 3}>
                    {save.isPending ? "Saving…" : "Save plan"}
                  </Button>
                ) : (
                  canManage && (
                    <Button type="button" onClick={() => setEditing(true)}>
                      <Pencil />
                      Edit {draft.plan} plan
                    </Button>
                  )
                )}
              </SlideoverFooter>
            </form>
          </SlideoverContent>
        </Slideover>
      )}
    </div>
  );
}

function Billing({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState("");
  const [reconcileId, setReconcileId, reconcileOpen, reconcileComplete] = useSlideoverState<
    string | null
  >(null);
  const subscriptions = useControlPage("/api/control/v1/billing", { status }, true, "subs");
  const checkouts = useControlPage("/api/control/v1/billing/checkouts", {}, true, "checkouts");
  const events = useControlPage("/api/control/v1/billing/events", {}, true, "events");
  const mutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      controlFetch(`/api/control/v1/billing/${id}/reconcile`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Billing reconciled");
      setReconcileId(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't reconcile billing."),
  });
  const canReconcile = hasControlCapability(session, "platform.billing.reconcile");

  return (
    <div className="space-y-8">
      <ConsoleHeader
        title="Billing"
        description="Subscriptions, checkouts, and provider events. Reconcile a tenant when webhooks were missed."
        onRefresh={() => {
          void subscriptions.refetch();
          void checkouts.refetch();
          void events.refetch();
        }}
        isFetching={subscriptions.isFetching}
      />
      <Select value={status} onValueChange={(value) => setStatus(value ?? "")}>
        <SelectTrigger className="w-full max-w-xs rounded-lg">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="">All subscription statuses</SelectItem>
          <SelectItem value="active">active</SelectItem>
          <SelectItem value="past_due">past_due</SelectItem>
          <SelectItem value="non_renewing">non_renewing</SelectItem>
          <SelectItem value="canceled">canceled</SelectItem>
        </SelectContent>
      </Select>
      <CollectionBlock
        title="Subscriptions"
        session={session}
        query={subscriptions}
        headers={["Account", "Reference", "Plan", "Status", "Period end"]}
        actions={(row) =>
          canReconcile
            ? [
                {
                  key: "reconcile",
                  label: "Reconcile billing",
                  icon: CircleDollarSign,
                  onClick: () => setReconcileId(cellString(row.account_id)),
                },
              ]
            : []
        }
        cells={(row) => (
          <>
            <TableCell className="px-4 py-3">
              <Link
                to={`/control/accounts/${cellText(row.account_id)}`}
                className="font-mono text-3xs text-primary hover:underline"
              >
                {cellText(row.account_id)}
              </Link>
            </TableCell>
            <TableCell className="px-4 py-3">{cellText(row.provider_subscription_code)}</TableCell>
            <TableCell className="px-4 py-3">
              <StatusPill value={cellText(row.plan)} />
            </TableCell>
            <TableCell className="px-4 py-3">
              <StatusPill value={cellText(row.status)} />
            </TableCell>
            <TableCell className="px-4 py-3 text-muted-foreground">
              {formatControlDate(cellString(row.current_period_end))}
            </TableCell>
          </>
        )}
      />
      <CollectionBlock
        title="Checkouts"
        session={session}
        query={checkouts}
        headers={["Account", "Reference", "Type", "Plan", "Status"]}
        cells={(row) => (
          <>
            <TableCell className="px-4 py-3 font-mono text-3xs">
              {cellText(row.account_id)}
            </TableCell>
            <TableCell className="px-4 py-3">{cellText(row.provider_reference)}</TableCell>
            <TableCell className="px-4 py-3">{cellText(row.checkout_type)}</TableCell>
            <TableCell className="px-4 py-3">
              <StatusPill value={cellText(row.plan)} />
            </TableCell>
            <TableCell className="px-4 py-3">
              <StatusPill value={cellText(row.status)} />
            </TableCell>
          </>
        )}
      />
      <CollectionBlock
        title="Provider events"
        session={session}
        query={events}
        headers={["Event", "Status", "Account", "Received"]}
        cells={(row) => (
          <>
            <TableCell className="px-4 py-3 font-mono text-2xs">
              {cellText(row.event_type)}
            </TableCell>
            <TableCell className="px-4 py-3">
              <StatusPill value={cellText(row.status)} />
            </TableCell>
            <TableCell className="px-4 py-3 font-mono text-3xs">
              {cellText(row.account_id, "—")}
            </TableCell>
            <TableCell className="px-4 py-3 text-muted-foreground">
              {formatControlDate(cellString(row.received_at))}
            </TableCell>
          </>
        )}
      />
      {reconcileId && (
        <ActionDialog
          open={reconcileOpen}
          onOpenChangeComplete={reconcileComplete}
          onOpenChange={(open) => !open && setReconcileId(null)}
          title="Reconcile billing"
          description="Fetch Paystack state and repair local subscription and checkout records for this account."
          icon={CircleDollarSign}
          targetDetails={[{ label: "Account ID", value: reconcileId, mono: true }]}
          requireReason
          confirmLabel="Reconcile"
          isLoading={mutation.isPending}
          onConfirm={(reason) => mutation.mutate({ id: reconcileId, reason })}
        />
      )}
    </div>
  );
}

function CollectionBlock({
  title,
  query,
  headers,
  cells,
  session,
  actions,
}: {
  title: string;
  query: ReturnType<typeof useControlPage>;
  headers: string[];
  cells: (row: ControlRow) => ReactNode;
  session?: ControlSession;
  actions?: (row: ControlRow) => RowActionItem[];
}) {
  const rows = query.rows;
  return (
    <div className="space-y-3">
      <h2 className="text-sm font-semibold">{title}</h2>
      {rows.length ? (
        <>
          <ControlTable headers={[...headers, "Actions"]}>
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                {cells(row)}
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions row={row} session={session} items={actions?.(row)} />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
    </div>
  );
}

function Credentials({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [credTarget, setCredTarget, credentialOpen, credentialComplete] = useSlideoverState<{
    id: string;
    name: string;
    prefix: string;
    audience: string;
    principalId: string;
  } | null>(null);
  const query = useControlPage("/api/control/v1/credentials");
  const rows = query.rows;
  const mutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      controlFetch(`/api/control/v1/credentials/${id}/revoke`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Credential revoked");
      setCredTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't revoke the credential."),
  });
  const canRevoke = hasControlCapability(session, "platform.credentials.revoke");

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Credentials"
        description="Inspect audience, assurance, and revocation state without exposing secrets."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      {rows.length ? (
        <>
          <ControlTable
            headers={["Prefix / name", "Audience", "Assurance", "Account", "Status", "Actions"]}
          >
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                <TableCell className="px-4 py-3">
                  <p className="font-semibold">{cellText(row.name, "Session")}</p>
                  <p className="mt-1 font-mono text-3xs text-muted-foreground">
                    {cellString(row.key_prefix) || cellString(row.id)}
                  </p>
                </TableCell>
                <TableCell className="px-4 py-3 text-2xs">{cellText(row.audience)}</TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={cellText(row.assurance)} />
                </TableCell>
                <TableCell className="px-4 py-3">
                  {row.account_id ? (
                    <Link
                      to={`/control/accounts/${cellText(row.account_id)}`}
                      className="font-mono text-3xs text-primary hover:underline"
                    >
                      {cellText(row.account_id)}
                    </Link>
                  ) : (
                    "—"
                  )}
                </TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={row.revoked_at ? "revoked" : "active"} />
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions
                    row={row}
                    session={session}
                    items={
                      canRevoke && !row.revoked_at
                        ? [
                            {
                              key: "revoke",
                              label: "Revoke credential",
                              icon: KeyRound,
                              variant: "destructive",
                              onClick: () =>
                                setCredTarget({
                                  id: cellString(row.id),
                                  name: cellText(row.name, "Session"),
                                  prefix: cellString(row.key_prefix) || cellString(row.id),
                                  audience: cellString(row.audience),
                                  principalId: cellString(row.principal_id),
                                }),
                            },
                          ]
                        : []
                    }
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
      {credTarget && (
        <ActionDialog
          open={credentialOpen}
          onOpenChangeComplete={credentialComplete}
          onOpenChange={(open) => !open && setCredTarget(null)}
          title="Revoke credential"
          description="Revoking immediately invalidates this API key or session."
          icon={KeyRound}
          tone="danger"
          targetDetails={[
            { label: "Name", value: credTarget.name },
            { label: "Prefix / ID", value: credTarget.prefix, mono: true },
            { label: "Principal ID", value: credTarget.principalId, mono: true },
            { label: "Audience", value: credTarget.audience },
          ]}
          requireReason
          confirmLabel="Revoke credential"
          isLoading={mutation.isPending}
          onConfirm={(reason) => mutation.mutate({ id: credTarget.id, reason })}
        />
      )}
    </div>
  );
}

function probeHealth(
  row: ControlRow,
  now: number,
): { label: string; tone: "good" | "warn" | "danger" | "muted" } {
  if (row.revoked_at) return { label: "Revoked", tone: "muted" };
  const seen = row.last_seen_at ? new Date(cellString(row.last_seen_at)).getTime() : 0;
  if (!seen) return { label: "Never connected", tone: "warn" };
  return now - seen <= 2 * 60_000
    ? { label: "Online", tone: "good" }
    : { label: "Offline", tone: "danger" };
}

function Probes() {
  const queryClient = useQueryClient();
  const now = useNow();
  const query = useControlPage("/api/control/v1/probes");
  const rows = query.rows;
  const [form, setForm] = useState({ region: "", name: "", reason: "" });
  const [created, setCreated] = useState<{
    name: string;
    region: string;
    token: string;
    endpoint: string;
  } | null>(null);
  const [revokeTarget, setRevokeTarget, revokeOpen, revokeComplete] =
    useSlideoverState<ControlRow | null>(null);

  const createMutation = useMutation({
    mutationFn: () =>
      controlFetch<{ data: { name: string; region: string; token: string; endpoint: string } }>(
        "/api/control/v1/probes",
        {
          method: "POST",
          body: JSON.stringify(form),
        },
      ),
    onSuccess: (result) => {
      setCreated(result.data);
      setForm({ region: "", name: "", reason: "" });
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "The probe could not be created."),
  });
  const revokeMutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      controlFetch(`/api/control/v1/probes/${id}/revoke`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Probe revoked");
      setRevokeTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "The probe could not be revoked."),
  });

  const runCommand = created
    ? `docker run -d --restart unless-stopped --name probe-${created.region} \\\n  -e PROBE_ENDPOINT=${created.endpoint} \\\n  -e PROBE_TOKEN=${created.token} \\\n  ghcr.io/thekiharani/monitor-cloud:latest probe`
    : "";

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Probes"
        description="Remote probes check monitors from their region. Operators are emailed when one stops reporting for five minutes."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />

      {created ? (
        <Card className="ring-status-up/40">
          <CardHeader>
            <CardTitle className="text-sm font-semibold">
              {created.name} is ready to run in {created.region}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <p className="text-xs text-muted-foreground">
              This token is shown once. Start the probe on a host in {created.region}; it appears as
              online within a minute.
            </p>
            <pre className="overflow-x-auto rounded-lg bg-muted p-3 font-mono text-2xs leading-relaxed">
              {runCommand}
            </pre>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  void navigator.clipboard
                    .writeText(runCommand)
                    .then(() => toast.success("Command copied"));
                }}
              >
                Copy command
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setCreated(null)}>
                Done
              </Button>
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-semibold">Add a probe</CardTitle>
          </CardHeader>
          <CardContent>
            <form
              className="grid grid-cols-[minmax(0,1fr)] gap-3 md:grid-cols-[10rem_12rem_minmax(0,1fr)_auto]"
              onSubmit={(event) => {
                event.preventDefault();
                createMutation.mutate();
              }}
            >
              <Input
                aria-label="Region"
                placeholder="Region, e.g. eu-west"
                value={form.region}
                onChange={(event) => setForm({ ...form, region: event.target.value.toLowerCase() })}
              />
              <Input
                aria-label="Probe name"
                placeholder="Name, e.g. eu-west-1"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
              />
              <Input
                aria-label="Reason"
                placeholder="Reason, kept in the audit trail"
                value={form.reason}
                onChange={(event) => setForm({ ...form, reason: event.target.value })}
              />
              <Button
                type="submit"
                disabled={
                  createMutation.isPending ||
                  !form.region.trim() ||
                  !form.name.trim() ||
                  form.reason.trim().length < 3
                }
              >
                {createMutation.isPending ? "Creating…" : "Create probe"}
              </Button>
            </form>
            <p className="mt-2 text-2xs text-muted-foreground">
              A region only serves customers once it is listed in PROBE_REGIONS on the server.
            </p>
          </CardContent>
        </Card>
      )}

      {rows.length ? (
        <>
          <ControlTable headers={["Probe", "Region", "State", "Last report", "Version", "Actions"]}>
            {rows.map((row) => {
              const health = probeHealth(row, now);
              return (
                <TableRow key={cellString(row.id)}>
                  <TableCell className="px-4 py-3">
                    <p className="font-medium">{cellText(row.name)}</p>
                    <p className="mt-0.5 font-mono text-3xs text-muted-foreground">
                      {cellText(row.key_prefix)}…
                    </p>
                  </TableCell>
                  <TableCell className="px-4 py-3 font-mono text-2xs">
                    {cellText(row.region)}
                  </TableCell>
                  <TableCell className="px-4 py-3">
                    <StateChip label={health.label} tone={health.tone} />
                  </TableCell>
                  <TableCell className="px-4 py-3 text-2xs text-muted-foreground">
                    {row.last_seen_at ? formatRelative(cellString(row.last_seen_at), now) : "Never"}
                    {row.last_ip ? (
                      <span className="ml-1 font-mono">· {cellText(row.last_ip)}</span>
                    ) : null}
                  </TableCell>
                  <TableCell className="px-4 py-3 font-mono text-2xs">
                    {cellText(row.version)}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right">
                    <ControlRowActions
                      row={row}
                      items={
                        !row.revoked_at
                          ? [
                              {
                                key: "revoke",
                                label: "Revoke probe",
                                icon: Ban,
                                variant: "destructive",
                                onClick: () => setRevokeTarget(row),
                              },
                            ]
                          : []
                      }
                    />
                  </TableCell>
                </TableRow>
              );
            })}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty label="No remote probes yet. Checks run from the built-in probe only." />
      )}

      {revokeTarget && (
        <ActionDialog
          open={revokeOpen}
          onOpenChangeComplete={revokeComplete}
          onOpenChange={(open) => !open && setRevokeTarget(null)}
          title="Revoke probe"
          description="The probe's next request is refused and its region stops checking unless another probe serves it."
          icon={RadioTower}
          tone="danger"
          targetDetails={[
            { label: "Name", value: cellText(revokeTarget.name) },
            { label: "Region", value: cellText(revokeTarget.region), mono: true },
          ]}
          requireReason
          confirmLabel="Revoke probe"
          isLoading={revokeMutation.isPending}
          onConfirm={(reason) => revokeMutation.mutate({ id: cellString(revokeTarget.id), reason })}
        />
      )}
    </div>
  );
}

function AccountLink({ id, plan }: { id: string; plan?: string }) {
  if (!id) return <span className="text-muted-foreground">—</span>;
  return (
    <Link
      to={`/control/accounts/${id}`}
      className="inline-flex items-center gap-1.5 font-mono text-2xs text-primary hover:underline"
    >
      {id.slice(0, 8)}…{plan && <span className="text-muted-foreground">· {plan}</span>}
    </Link>
  );
}

function StateChip({ label, tone }: { label: string; tone: "good" | "warn" | "danger" | "muted" }) {
  const styles = {
    good: "border-success/30 bg-success/10 text-success",
    warn: "border-warning/30 bg-warning/10 text-warning",
    danger: "border-destructive/30 bg-destructive/10 text-destructive",
    muted: "border-border bg-muted text-muted-foreground",
  } as const;
  return (
    <span
      className={`inline-flex rounded-md border px-1.5 py-0.5 text-2xs font-medium ${styles[tone]}`}
    >
      {label}
    </span>
  );
}

function Team({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [accountId, setAccountId] = useState("");
  const debounced = useDebouncedValue(accountId);
  const [view, setView] = useFilterParam("view", "members");
  const [target, setTarget, targetOpen, targetComplete] = useSlideoverState<{
    accountId: string;
    principalId: string;
    email: string;
    role: string;
  } | null>(null);

  const members = useControlPage("/api/control/v1/members", { account_id: debounced });
  const invites = useControlPage("/api/control/v1/invites", {
    account_id: debounced,
    status: "pending",
  });
  const query = view === "members" ? members : invites;

  const mutation = useMutation({
    mutationFn: ({
      account,
      principal,
      reason,
    }: {
      account: string;
      principal: string;
      reason: string;
    }) =>
      controlFetch(`/api/control/v1/accounts/${account}/members/${principal}`, {
        method: "DELETE",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Member removed and their credentials revoked");
      setTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't remove that member."),
  });
  const canRemove = hasControlCapability(session, "platform.members.remove");

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Team access"
        description="Who can reach each workspace, and which invitations are still outstanding."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <div className="flex flex-wrap gap-2">
        <Select value={view} onValueChange={(value) => setView(value ?? "")}>
          <SelectTrigger className="w-full max-w-xs rounded-lg">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="members">Members</SelectItem>
            <SelectItem value="invites">Pending invitations</SelectItem>
          </SelectContent>
        </Select>
        <Input
          size="xl"
          value={accountId}
          onChange={(event) => setAccountId(event.target.value)}
          placeholder="Filter by account ID"
          className="max-w-xs font-mono text-xs"
        />
      </div>
      {query.rows.length ? (
        <>
          {view === "members" ? (
            <ControlTable headers={["Email", "Account", "Role", "Status", "Joined", "Actions"]}>
              {query.rows.map((row) => (
                <TableRow key={cellString(row.principal_id) + cellString(row.account_id)}>
                  <TableCell className="px-4 py-3">{cellText(row.email, "Unknown")}</TableCell>
                  <TableCell className="px-4 py-3">
                    <AccountLink id={cellString(row.account_id)} plan={cellString(row.plan)} />
                  </TableCell>
                  <TableCell className="px-4 py-3">
                    <StateChip
                      label={cellString(row.role)}
                      tone={cellString(row.role) === "owner" ? "good" : "muted"}
                    />
                  </TableCell>
                  <TableCell className="px-4 py-3 text-muted-foreground">
                    {cellText(row.status)}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-muted-foreground">
                    {formatControlDate(cellString(row.created_at))}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right">
                    <ControlRowActions
                      row={row}
                      session={session}
                      items={
                        canRemove && cellString(row.role) !== "owner" && !row.deleted_at
                          ? [
                              {
                                key: "remove",
                                label: "Remove member",
                                icon: Ban,
                                variant: "destructive",
                                onClick: () =>
                                  setTarget({
                                    accountId: cellString(row.account_id),
                                    principalId: cellString(row.principal_id),
                                    email: cellString(row.email),
                                    role: cellString(row.role),
                                  }),
                              },
                            ]
                          : []
                      }
                    />
                  </TableCell>
                </TableRow>
              ))}
            </ControlTable>
          ) : (
            <ControlTable headers={["Invited", "Account", "Role", "Expires", "Sent", "Actions"]}>
              {query.rows.map((row) => (
                <TableRow key={cellString(row.id)}>
                  <TableCell className="px-4 py-3">{cellText(row.email)}</TableCell>
                  <TableCell className="px-4 py-3">
                    <AccountLink id={cellString(row.account_id)} />
                  </TableCell>
                  <TableCell className="px-4 py-3">{cellText(row.role)}</TableCell>
                  <TableCell className="px-4 py-3 text-muted-foreground">
                    {formatControlDate(cellString(row.expires_at))}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-muted-foreground">
                    {formatControlDate(cellString(row.created_at))}
                  </TableCell>
                  <TableCell className="px-4 py-3 text-right">
                    <ControlRowActions row={row} session={session} />
                  </TableCell>
                </TableRow>
              ))}
            </ControlTable>
          )}
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
      {target && (
        <ActionDialog
          open={targetOpen}
          onOpenChangeComplete={targetComplete}
          onOpenChange={(open) => !open && setTarget(null)}
          title="Remove workspace member"
          description="They lose access immediately and every session and API key they hold for this account is revoked."
          icon={Ban}
          tone="danger"
          targetDetails={[
            { label: "Member", value: target.email || "Unknown" },
            { label: "Role", value: target.role },
            { label: "Account ID", value: target.accountId, mono: true },
          ]}
          requireReason
          confirmLabel="Remove member"
          isLoading={mutation.isPending}
          onConfirm={(reason) =>
            mutation.mutate({ account: target.accountId, principal: target.principalId, reason })
          }
        />
      )}
    </div>
  );
}

function Lifecycle() {
  const [status, setStatus] = useFilterParam("state", "pending");
  const query = useControlPage("/api/control/v1/lifecycle", { status });

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Account lifecycle"
        description="Closed accounts, and whether their data has been erased yet."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <Select value={status} onValueChange={(value) => setStatus(value ?? "")}>
        <SelectTrigger className="w-full max-w-xs rounded-lg">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="pending">Awaiting erasure</SelectItem>
          <SelectItem value="erased">Erased</SelectItem>
          <SelectItem value="">All closed</SelectItem>
        </SelectContent>
      </Select>
      {query.rows.length ? (
        <>
          <ControlTable
            headers={["Account", "Plan", "Closed", "Erased", "Credentials", "Members", "Actions"]}
          >
            {query.rows.map((row) => (
              <TableRow key={cellString(row.account_id)}>
                <TableCell className="px-4 py-3">
                  <AccountLink id={cellString(row.account_id)} />
                </TableCell>
                <TableCell className="px-4 py-3">{cellText(row.plan)}</TableCell>
                <TableCell className="px-4 py-3 text-muted-foreground">
                  {formatControlDate(cellString(row.closed_at))}
                </TableCell>
                <TableCell className="px-4 py-3">
                  {row.purged_at ? (
                    <StateChip label={formatControlDate(cellString(row.purged_at))} tone="good" />
                  ) : (
                    <StateChip label="Retained" tone="warn" />
                  )}
                </TableCell>
                <TableCell className="px-4 py-3 tabular-nums">
                  {cellText(row.credentials, "0")}
                </TableCell>
                <TableCell className="px-4 py-3 tabular-nums">
                  {cellText(row.memberships, "0")}
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions row={row} />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
    </div>
  );
}

function Jobs({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState("pending");
  const [jobTarget, setJobTarget, jobOpen, jobComplete] = useSlideoverState<{
    id: string;
    toEmail: string;
    template: string;
    attempts: number;
    lastError: string;
  } | null>(null);
  const query = useControlPage("/api/control/v1/email-jobs", { status });
  const rows = query.rows;
  const mutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      controlFetch(`/api/control/v1/email-jobs/${id}/retry`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Email job requeued");
      setJobTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't requeue the email job."),
  });
  const canRetry = hasControlCapability(session, "platform.jobs.retry");

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Email jobs"
        description="Inspect delivery backlogs and requeue failed work."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <Select value={status} onValueChange={(value) => setStatus(value ?? "")}>
        <SelectTrigger className="w-full max-w-xs rounded-lg">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="pending">Pending</SelectItem>
          <SelectItem value="sent">Sent</SelectItem>
          <SelectItem value="">All</SelectItem>
        </SelectContent>
      </Select>
      {rows.length ? (
        <>
          <ControlTable
            headers={["Recipient", "Template", "Attempts", "Next attempt", "Last error", "Actions"]}
          >
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                <TableCell className="px-4 py-3">{cellText(row.to_email)}</TableCell>
                <TableCell className="px-4 py-3 font-mono text-2xs">
                  {cellText(row.template)}
                </TableCell>
                <TableCell className="px-4 py-3">{cellText(row.attempts)}</TableCell>
                <TableCell className="px-4 py-3 text-muted-foreground">
                  {formatControlDate(cellString(row.next_attempt_at))}
                </TableCell>
                <TableCell className="max-w-[220px] truncate px-4 py-3 text-muted-foreground">
                  {cellText(row.last_error, "—")}
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions
                    row={row}
                    session={session}
                    items={
                      canRetry && !row.sent_at
                        ? [
                            {
                              key: "retry",
                              label: "Retry delivery",
                              icon: TicketCheck,
                              onClick: () =>
                                setJobTarget({
                                  id: cellString(row.id),
                                  toEmail: cellString(row.to_email),
                                  template: cellString(row.template),
                                  attempts: Number(row.attempts ?? 0),
                                  lastError: cellString(row.last_error),
                                }),
                            },
                          ]
                        : []
                    }
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty label="No email jobs" />
      )}
      {jobTarget && (
        <ActionDialog
          open={jobOpen}
          onOpenChangeComplete={jobComplete}
          onOpenChange={(open) => !open && setJobTarget(null)}
          title="Retry email delivery"
          description="Requeue this job for an immediate delivery attempt."
          icon={TicketCheck}
          targetDetails={[
            { label: "Recipient", value: jobTarget.toEmail },
            { label: "Template", value: jobTarget.template, mono: true },
            { label: "Prior attempts", value: String(jobTarget.attempts) },
            { label: "Last error", value: jobTarget.lastError || "None" },
          ]}
          requireReason
          confirmLabel="Retry delivery"
          isLoading={mutation.isPending}
          onConfirm={(reason) => mutation.mutate({ id: jobTarget.id, reason })}
        />
      )}
    </div>
  );
}

function Access({ session }: { session: ControlSession }) {
  const queryClient = useQueryClient();
  const [lookup, setLookup] = useState("");
  const debouncedLookup = useDebouncedValue(lookup);
  const [revokeId, setRevokeId, revokeOpen, revokeComplete] = useSlideoverState<ControlRow | null>(
    null,
  );
  const [form, setForm] = useState({
    principal_id: "",
    capability: session.known_capabilities[0] || "platform.accounts.read",
    scope_type: "platform",
    scope_id: "",
    reason: "",
  });
  const query = useControlPage("/api/control/v1/access-grants");
  const rows = query.rows;
  const matches = useControlData(
    controlListPath("/api/control/v1/accounts", { q: debouncedLookup, limit: 8 }),
    debouncedLookup.length >= 3 && hasControlCapability(session, "platform.accounts.read"),
  );
  const grantMutation = useMutation({
    mutationFn: () =>
      controlFetch("/api/control/v1/access-grants", {
        method: "POST",
        body: JSON.stringify({ ...form, scope_id: form.scope_id || undefined }),
      }),
    onSuccess: () => {
      toast.success("Access grant saved");
      setForm({ ...form, principal_id: "", scope_id: "", reason: "" });
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't save the access grant."),
  });
  const revokeMutation = useMutation({
    mutationFn: ({ id, reason }: { id: string; reason: string }) =>
      controlFetch(`/api/control/v1/access-grants/${id}/revoke`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Grant revoked");
      setRevokeId(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't revoke the grant."),
  });
  const canGrant = hasControlCapability(session, "platform.access.grant");

  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Access grants"
        description="Delegate atomic capabilities with explicit scope and reason. Revoke from the same list."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      {canGrant && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Grant capability</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-[minmax(0,1fr)] gap-3 md:grid-cols-2 xl:grid-cols-3">
            <div className="space-y-2">
              <Input
                placeholder="Lookup email to fill principal ID"
                value={lookup}
                onChange={(event) => setLookup(event.target.value)}
              />
              {matches.data?.slice(0, 5).map((row) => (
                <button
                  key={cellString(row.id)}
                  type="button"
                  className="block w-full rounded-lg border border-border px-3 py-2 text-left text-xs hover:bg-muted"
                  onClick={() => {
                    setForm({ ...form, principal_id: cellString(row.principal_id) });
                    setLookup(cellString(row.email) || cellString(row.principal_id));
                  }}
                >
                  <span className="font-medium">{cellText(row.email, "Unverified")}</span>
                  <span className="ml-2 font-mono text-3xs text-muted-foreground">
                    {cellText(row.principal_id)}
                  </span>
                </button>
              ))}
            </div>
            <Input
              placeholder="Principal ID"
              value={form.principal_id}
              onChange={(event) => setForm({ ...form, principal_id: event.target.value })}
            />
            <Select
              value={form.capability}
              onValueChange={(value) => setForm({ ...form, capability: value ?? "" })}
            >
              <SelectTrigger className="w-full rounded-lg">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {session.known_capabilities.map((capability) => (
                  <SelectItem key={capability} value={capability}>
                    {capability}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={form.scope_type}
              onValueChange={(value) => setForm({ ...form, scope_type: value ?? "" })}
            >
              <SelectTrigger className="w-full rounded-lg">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="platform">Platform scope</SelectItem>
                <SelectItem value="account">Account scope</SelectItem>
              </SelectContent>
            </Select>
            <Input
              placeholder="Account scope ID"
              value={form.scope_id}
              onChange={(event) => setForm({ ...form, scope_id: event.target.value })}
              disabled={form.scope_type !== "account"}
            />
            <div className="flex gap-2">
              <Input
                placeholder="Reason (min 3 characters)"
                value={form.reason}
                onChange={(event) => setForm({ ...form, reason: event.target.value })}
              />
              <Button
                className="rounded-lg"
                disabled={grantMutation.isPending || !form.principal_id || form.reason.length < 3}
                onClick={() => grantMutation.mutate()}
              >
                Grant
              </Button>
            </div>
          </CardContent>
        </Card>
      )}
      {rows.length ? (
        <>
          <ControlTable
            headers={["Principal", "Capability", "Scope", "Reason", "Status", "Actions"]}
          >
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                <TableCell className="px-4 py-3 font-mono text-3xs">
                  {cellText(row.principal_id)}
                </TableCell>
                <TableCell className="px-4 py-3 font-mono text-2xs">
                  {cellText(row.capability)}
                </TableCell>
                <TableCell className="px-4 py-3">
                  {cellText(row.scope_type)}
                  {row.scope_id ? ` · ${cellText(row.scope_id)}` : ""}
                </TableCell>
                <TableCell className="max-w-[260px] truncate px-4 py-3 text-muted-foreground">
                  {cellText(row.reason)}
                </TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={row.revoked_at ? "revoked" : "active"} />
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions
                    row={row}
                    session={session}
                    items={
                      canGrant && !row.revoked_at
                        ? [
                            {
                              key: "revoke",
                              label: "Revoke grant",
                              icon: ShieldCheck,
                              variant: "destructive",
                              onClick: () => setRevokeId(row),
                            },
                          ]
                        : []
                    }
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty label="No access grants found" />
      )}
      {revokeId && (
        <ActionDialog
          open={revokeOpen}
          onOpenChangeComplete={revokeComplete}
          onOpenChange={(open) => !open && setRevokeId(null)}
          title="Revoke access grant"
          description="The principal will lose this capability immediately."
          icon={ShieldCheck}
          tone="danger"
          targetDetails={[
            { label: "Principal", value: cellString(revokeId.principal_id), mono: true },
            { label: "Capability", value: cellString(revokeId.capability), mono: true },
          ]}
          requireReason
          confirmLabel="Revoke grant"
          isLoading={revokeMutation.isPending}
          onConfirm={(reason) => revokeMutation.mutate({ id: cellString(revokeId.id), reason })}
        />
      )}
    </div>
  );
}

function Audit() {
  const [search, setSearch] = useState("");
  const debounced = useDebouncedValue(search);
  const query = useControlPage("/api/control/v1/audit-events", { q: debounced });
  const rows = query.rows;
  return (
    <div className="space-y-6">
      <ConsoleHeader
        title="Audit trail"
        description="Append-only evidence of control-plane decisions. Search by principal or target ID."
        onRefresh={() => void query.refetch()}
        isFetching={query.isFetching}
      />
      <Input
        className="max-w-md rounded-lg"
        placeholder="Principal or target ID"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      {rows.length ? (
        <>
          <ControlTable
            headers={["Action", "Capability", "Target", "Reason", "Actor", "Time", "Actions"]}
          >
            {rows.map((row) => (
              <TableRow key={cellString(row.id)}>
                <TableCell className="px-4 py-3 font-semibold">{cellText(row.action)}</TableCell>
                <TableCell className="px-4 py-3 font-mono text-3xs">
                  {cellText(row.capability)}
                </TableCell>
                <TableCell className="px-4 py-3">
                  {cellText(row.target_type)} · {cellText(row.target_id, "—")}
                </TableCell>
                <TableCell className="max-w-[240px] truncate px-4 py-3 text-muted-foreground">
                  {cellText(row.reason)}
                </TableCell>
                <TableCell className="px-4 py-3 font-mono text-3xs text-muted-foreground">
                  {cellText(row.principal_id)}
                </TableCell>
                <TableCell className="px-4 py-3 text-muted-foreground">
                  {formatControlDate(cellString(row.created_at))}
                </TableCell>
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions
                    row={row}
                    label={`${cellText(row.action)} ${cellString(row.id)}`}
                  />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty />
      )}
    </div>
  );
}

function SignInFirst({ returnTo }: { returnTo: string }) {
  useEffect(() => {
    window.location.replace(`/login?next=${encodeURIComponent(returnTo)}`);
  }, [returnTo]);

  return (
    <main className="grid min-h-screen place-items-center bg-background">
      <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
    </main>
  );
}

function NotAnOperator() {
  usePageMetadata({
    title: "Control plane",
    description: "This account holds no platform capabilities.",
  });

  return (
    <main className="grid min-h-screen place-items-center bg-background p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="space-y-4 p-8">
          <Logo />
          <div>
            <p className="text-xs font-medium text-muted-foreground">Control plane</p>
            <CardTitle className="mt-2 text-2xl">No platform capabilities</CardTitle>
            <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
              You are signed in, and this account holds no grant on the platform. Signing in again
              will not change that — a platform owner has to grant the capability first.
            </p>
          </div>
        </CardHeader>
        <CardContent className="p-8 pt-0">
          <Button size="xl" className="w-full" render={<Link to="/app" />}>
            Back to your dashboard
          </Button>
        </CardContent>
      </Card>
    </main>
  );
}

function ControlSidebar({
  session,
  destinations,
  onLogout,
}: {
  session: ControlSession;
  destinations: NavDestination[];
  onLogout: () => void;
}) {
  const location = useLocation();

  return (
    <Sidebar collapsible="icon">
      <SidebarHeader className="gap-3 p-3">
        <div className="flex h-8 items-center px-1 group-data-[collapsible=icon]:justify-center">
          <Logo className="group-data-[collapsible=icon]:[&>span:last-child]:hidden" />
        </div>

        <div className="flex items-center gap-3 rounded-xl bg-primary/5 p-3 ring-1 ring-primary/20 group-data-[collapsible=icon]:hidden">
          <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
            <ShieldCheck className="size-4" />
          </span>
          <div className="min-w-0">
            <p className="text-xs font-medium">Control plane</p>
            <p className="truncate text-2xs text-muted-foreground">
              {session.email || "Capability protected"}
            </p>
          </div>
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Operations</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {destinations.map((destination) => (
                <SidebarMenuItem key={destination.to}>
                  <SidebarMenuButton
                    isActive={isDestinationActive(destination, location.pathname)}
                    tooltip={destination.label}
                    render={<NavLink to={destination.to} />}
                  >
                    <destination.icon />
                    <span>{destination.label}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter className="p-3">
        <SidebarSeparator className="mx-0" />
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              tooltip="Sign out"
              onClick={onLogout}
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <LogOut />
              <span>Sign out</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarFooter>

      <SidebarRail />
    </Sidebar>
  );
}

export function Control() {
  const [commandMenuOpen, setCommandMenuOpen] = useState(false);
  const isMobile = useIsMobile();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const sessionQuery = useControlSession();
  const admission = sessionQuery.data;
  const session = admission?.state === "admitted" ? admission.session : undefined;

  usePageMetadata(
    session
      ? getPageMetadata(location.pathname)
      : {
          title: "Control plane",
          description: "Secure operator access to the Noria Monitor control plane.",
        },
  );

  const logout = async () => {
    await api<void>(endpoints.auth.logout, { method: "POST" }).catch(() => undefined);
    queryClient.clear();
    void navigate("/login");
  };

  if (sessionQuery.isLoading) {
    return (
      <main className="grid min-h-screen place-items-center bg-background">
        <div className="size-8 animate-spin rounded-full border-2 border-primary border-t-transparent" />
      </main>
    );
  }
  if (sessionQuery.isError) {
    return <ErrorPage code={500} />;
  }
  if (!admission || admission.state === "unauthenticated") {
    return <SignInFirst returnTo={location.pathname} />;
  }
  if (!session) {
    return <NotAnOperator />;
  }

  const operatorAccount: Account = {
    id: session.account_id || session.principal_id,
    email: session.email || session.principal_id,
    plan: "free",
    status: "active",
    role: "owner",
    name: "Control plane",
    display_name: session.email || session.principal_id,
    timezone: "",
  };
  const destinations = controlNavFor((capability) => hasControlCapability(session, capability));
  const commandItems: CommandItemDef[] = destinations.map((destination) => ({
    label: destination.label,
    icon: destination.icon,
    path: destination.to,
  }));

  return (
    <SidebarProvider>
      {!isMobile && (
        <ControlSidebar
          session={session}
          destinations={destinations}
          onLogout={() => void logout()}
        />
      )}

      <SidebarInset className="min-w-0">
        <TopNavbar
          account={operatorAccount}
          workspace="control"
          canAccessControl
          onOpenCommandMenu={() => setCommandMenuOpen(true)}
        />
        <div className="mx-auto w-full max-w-[1500px] px-4 py-6 sm:px-8 sm:py-10">
          <Routes>
            <Route index element={<Navigate to="overview" replace />} />
            <Route path="overview" element={<Overview session={session} />} />
            <Route
              path="accounts"
              element={
                <RequireCapability session={session} capability="platform.accounts.read">
                  <Accounts session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="accounts/:id"
              element={
                <RequireCapability session={session} capability="platform.accounts.read">
                  <AccountWorkspace session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="usage"
              element={
                <RequireCapability session={session} capability="platform.usage.read">
                  <Usage />
                </RequireCapability>
              }
            />
            <Route
              path="plans"
              element={
                <RequireCapability session={session} capability="platform.billing.read">
                  <Plans session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="billing"
              element={
                <RequireCapability session={session} capability="platform.billing.read">
                  <Billing session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="team"
              element={
                <RequireCapability session={session} capability="platform.members.read">
                  <Team session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="lifecycle"
              element={
                <RequireCapability session={session} capability="platform.accounts.read">
                  <Lifecycle />
                </RequireCapability>
              }
            />
            <Route
              path="credentials"
              element={
                <RequireCapability session={session} capability="platform.credentials.read">
                  <Credentials session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="probes"
              element={
                <RequireCapability session={session} capability="platform.probes.manage">
                  <Probes />
                </RequireCapability>
              }
            />
            <Route
              path="jobs"
              element={
                <RequireCapability session={session} capability="platform.jobs.read">
                  <Jobs session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="access"
              element={
                <RequireCapability session={session} capability="platform.access.read">
                  <Access session={session} />
                </RequireCapability>
              }
            />
            <Route
              path="audit"
              element={
                <RequireCapability session={session} capability="platform.audit.read">
                  <Audit />
                </RequireCapability>
              }
            />
            <Route path="*" element={<ErrorPage code={404} embedded />} />
          </Routes>
        </div>

        {isMobile && <div aria-hidden="true" className="h-24 shrink-0" />}
      </SidebarInset>

      {isMobile && (
        <MobileTabBar
          workspace="control"
          destinations={destinations}
          tabDestinations={controlTabBar}
          canAccessControl
          onSignOut={() => void logout()}
        />
      )}

      <CommandMenu
        open={commandMenuOpen}
        onOpenChange={setCommandMenuOpen}
        workspace="control"
        canAccessControl
        navigationItems={commandItems}

        onSignOut={() => void logout()}
      />
    </SidebarProvider>
  );
}
