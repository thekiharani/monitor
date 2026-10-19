import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, Ban, CheckCircle2, CircleDollarSign, Gift, KeyRound } from "lucide-react";
import { toast } from "sonner";
import { errorToast } from "@/lib/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ActionDialog } from "@/components/common/ActionDialog";
import { useSlideoverState } from "@/components/ui/slideover";
import {
  controlFetch,
  hasControlCapability,
  type ControlAccount,
  type ControlRow,
  cellText,
  cellString,
  type ControlSession,
} from "@/control-api";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { TableCell, TableRow } from "@/components/ui/table";
import {
  ConsoleHeader,
  ControlEmpty,
  ControlPager,
  ControlTable,
  ControlRowActions,
  Metric,
  StatusPill,
  formatControlDate,
  useControlPage,
} from "@/pages/control/shared";

export function AccountWorkspace({ session }: { session: ControlSession }) {
  const { id = "" } = useParams();
  const queryClient = useQueryClient();
  const [statusTarget, setStatusTarget, statusOpen, statusComplete] = useSlideoverState<
    "active" | "suspended" | "closed" | null
  >(null);
  const [reconcileOpen, setReconcileOpen, reconcileVisible, reconcileComplete] =
    useSlideoverState(false);
  const [complimentaryOpen, setComplimentaryOpen, complimentaryVisible, complimentaryComplete] =
    useSlideoverState(false);
  const [
    revokeComplimentaryOpen,
    setRevokeComplimentaryOpen,
    revokeComplimentaryVisible,
    revokeComplimentaryComplete,
  ] = useSlideoverState(false);
  const [complimentaryPlan, setComplimentaryPlan] = useState("pro");
  const [complimentaryExpiry, setComplimentaryExpiry] = useState("");

  const accountQuery = useQuery({
    queryKey: ["control", "account", id],
    queryFn: () =>
      controlFetch<{ data: ControlAccount }>(`/api/control/v1/accounts/${id}`).then(
        (body) => body.data,
      ),
    enabled: Boolean(id),
  });
  const account = accountQuery.data;
  const usage = useControlPage(
    "/api/control/v1/usage",
    { account_id: id },
    hasControlCapability(session, "platform.usage.read") && Boolean(id),
    "usage",
  );
  const members = useControlPage(
    "/api/control/v1/members",
    { account_id: id },
    hasControlCapability(session, "platform.members.read") && Boolean(id),
    "members",
  );
  const invites = useControlPage(
    "/api/control/v1/invites",
    { account_id: id, status: "pending" },
    hasControlCapability(session, "platform.members.read") && Boolean(id),
    "invites",
  );
  const credentials = useControlPage(
    "/api/control/v1/credentials",
    { account_id: id },
    hasControlCapability(session, "platform.credentials.read") && Boolean(id),
    "credentials",
  );
  const billing = useControlPage(
    "/api/control/v1/billing",
    { account_id: id },
    hasControlCapability(session, "platform.billing.read") && Boolean(id),
    "billing",
  );
  const checkouts = useControlPage(
    "/api/control/v1/billing/checkouts",
    { account_id: id },
    hasControlCapability(session, "platform.billing.read") && Boolean(id),
    "checkouts",
  );
  const events = useControlPage(
    "/api/control/v1/billing/events",
    { account_id: id },
    hasControlCapability(session, "platform.billing.read") && Boolean(id),
    "events",
  );
  const audit = useControlPage(
    "/api/control/v1/audit-events",
    { account_id: id },
    hasControlCapability(session, "platform.audit.read") && Boolean(id),
    "audit",
  );

  const statusMutation = useMutation({
    mutationFn: ({ status, reason }: { status: string; reason: string }) =>
      controlFetch(`/api/control/v1/accounts/${id}/status`, {
        method: "PATCH",
        body: JSON.stringify({ status, reason }),
      }),
    onSuccess: () => {
      toast.success("Account status updated");
      setStatusTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't update the account."),
  });
  const reconcileMutation = useMutation({
    mutationFn: (reason: string) =>
      controlFetch(`/api/control/v1/billing/${id}/reconcile`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Billing reconciled");
      setReconcileOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't reconcile billing."),
  });
  const grantComplimentaryMutation = useMutation({
    mutationFn: (reason: string) =>
      controlFetch(`/api/control/v1/billing/${id}/complimentary`, {
        method: "POST",
        body: JSON.stringify({
          plan: complimentaryPlan,
          expires_at: complimentaryExpiry ? new Date(complimentaryExpiry).toISOString() : "",
          reason,
        }),
      }),
    onSuccess: () => {
      toast.success("Complimentary plan granted");
      setComplimentaryOpen(false);
      setComplimentaryExpiry("");
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't grant the complimentary plan."),
  });
  const revokeComplimentaryMutation = useMutation({
    mutationFn: (reason: string) =>
      controlFetch(`/api/control/v1/billing/${id}/complimentary`, {
        method: "DELETE",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Complimentary plan revoked");
      setRevokeComplimentaryOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["control"] });
    },
    onError: (error: Error) => errorToast(error, "We couldn't revoke the complimentary plan."),
  });

  if (accountQuery.isLoading) return <ControlEmpty label="Loading account…" />;
  if (!account) return <ControlEmpty label="Account not found" />;

  const closed = account.status === "closed";
  const canSuspend = hasControlCapability(session, "platform.accounts.suspend");
  const canClose = hasControlCapability(session, "platform.accounts.close");
  const canReconcile = hasControlCapability(session, "platform.billing.reconcile");
  const canComp = hasControlCapability(session, "platform.billing.comp");
  const complimentary = account.complimentary_plan ?? null;
  const complimentaryExpiresAt = account.complimentary_expires_at ?? null;

  return (
    <div className="space-y-8">
      <ConsoleHeader
        title={account.email || "Unverified principal"}
        description="Tenant workspace: lifecycle, usage, trust, billing, and audit in one place."
        onRefresh={() => {
          void accountQuery.refetch();
          void usage.refetch();
          void credentials.refetch();
          void billing.refetch();
          void checkouts.refetch();
          void events.refetch();
          void audit.refetch();
        }}
        isFetching={accountQuery.isFetching}
      />
      <p className="text-sm text-muted-foreground">
        <Link to="/control/accounts" className="font-medium text-primary hover:underline">
          All accounts
        </Link>
        <span className="mx-2">·</span>
        <span className="font-mono text-2xs">{account.id}</span>
        {account.principal_id && (
          <>
            <span className="mx-2">·</span>
            <span className="font-mono text-2xs">principal {account.principal_id}</span>
          </>
        )}
      </p>
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <Metric label="Plan" value={account.plan} icon={CircleDollarSign} />
        <Metric
          label="API requests this period"
          value={account.period_api_requests ?? "—"}
          icon={Activity}
        />
        <Metric
          label="Active credentials"
          value={account.active_credentials ?? "—"}
          icon={KeyRound}
          tone="success"
        />
      </div>
      <Card id="plan">
        <CardHeader>
          <CardTitle className="text-base">Plan &amp; subscription</CardTitle>
          <p className="text-xs text-muted-foreground">
            Assign a complimentary plan or manage its expiry. Paid subscriptions are managed by the
            account owner.
          </p>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center gap-3">
            <StatusPill value={account.plan} />
            {complimentary && (
              <span className="text-xs text-muted-foreground">
                Complimentary ·{" "}
                {complimentaryExpiresAt
                  ? `until ${formatControlDate(complimentaryExpiresAt)}`
                  : "no end date"}
              </span>
            )}
            <Link to="/control/plans" className="text-xs font-medium text-primary hover:underline">
              View plan catalog
            </Link>
          </div>
          <div className="flex flex-wrap gap-2">
            {" "}
            {canComp && !closed && !complimentary && (
              <Button
                variant="outline"
                size="sm"
                className="rounded-lg"
                onClick={() => setComplimentaryOpen(true)}
              >
                Grant complimentary plan
              </Button>
            )}
            {canComp && complimentary && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  className="rounded-lg"
                  onClick={() => setComplimentaryOpen(true)}
                >
                  Change complimentary plan
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="rounded-lg"
                  onClick={() => setRevokeComplimentaryOpen(true)}
                >
                  End complimentary plan
                </Button>
              </>
            )}
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Lifecycle</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-3">
          <span className="inline-flex items-center gap-1.5">
            <span className="text-2xs text-muted-foreground">Account</span>
            <StatusPill value={account.status} />
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="text-2xs text-muted-foreground">Subscription</span>
            {account.subscription_status ? (
              <StatusPill value={account.subscription_status} />
            ) : (
              <span className="text-2xs text-muted-foreground">none</span>
            )}
          </span>
          {complimentary && (
            <span className="inline-flex items-center gap-1.5">
              <span className="text-2xs text-muted-foreground">Complimentary</span>
              <StatusPill value={complimentary} />
              <span className="text-2xs text-muted-foreground">
                {complimentaryExpiresAt
                  ? `until ${formatControlDate(complimentaryExpiresAt)}`
                  : "no end date"}
              </span>
            </span>
          )}
          <span className="text-xs text-muted-foreground">
            Created {formatControlDate(account.created_at)}
          </span>
          <span className="ml-auto flex flex-wrap gap-2">
            {canSuspend && !closed && account.status !== "suspended" && (
              <Button
                variant="outline"
                size="sm"
                className="rounded-lg"
                onClick={() => setStatusTarget("suspended")}
              >
                Suspend
              </Button>
            )}
            {canSuspend && account.status === "suspended" && (
              <Button
                variant="outline"
                size="sm"
                className="rounded-lg"
                onClick={() => setStatusTarget("active")}
              >
                Reactivate
              </Button>
            )}
            {canClose && !closed && (
              <Button
                variant="outline"
                size="sm"
                className="rounded-lg"
                onClick={() => setStatusTarget("closed")}
              >
                Close
              </Button>
            )}
            {canReconcile && !closed && (
              <Button size="sm" className="rounded-lg" onClick={() => setReconcileOpen(true)}>
                Reconcile billing
              </Button>
            )}
          </span>
        </CardContent>
      </Card>
      {hasControlCapability(session, "platform.usage.read") && (
        <SectionTable
          title="Usage"
          headers={["Period", "API requests"]}
          query={usage}
          empty="No usage rows"
          cells={(row) => (
            <>
              <TableCell className="px-4 py-3">{cellText(row.period, "—")}</TableCell>
              <TableCell className="px-4 py-3">{cellText(row.api_requests, "0")}</TableCell>
            </>
          )}
        />
      )}
      {hasControlCapability(session, "platform.members.read") && (
        <SectionTable
          title="Team access"
          headers={["Member", "Role", "Status", "Joined"]}
          query={members}
          empty="No members"
          cells={(row) => (
            <>
              <TableCell className="px-4 py-3">
                {cellText(row.email, "Unverified principal")}
              </TableCell>
              <TableCell className="px-4 py-3">
                <StatusPill value={cellText(row.role)} />
              </TableCell>
              <TableCell className="px-4 py-3 text-muted-foreground">
                {cellText(row.status)}
              </TableCell>
              <TableCell className="px-4 py-3 text-muted-foreground">
                {formatControlDate(cellString(row.created_at))}
              </TableCell>
            </>
          )}
        />
      )}
      {hasControlCapability(session, "platform.members.read") && invites.rows.length > 0 && (
        <SectionTable
          title="Pending invitations"
          headers={["Invited", "Role", "Expires", "Sent"]}
          query={invites}
          empty="No pending invitations"
          cells={(row) => (
            <>
              <TableCell className="px-4 py-3">{cellText(row.email)}</TableCell>
              <TableCell className="px-4 py-3">{cellText(row.role)}</TableCell>
              <TableCell className="px-4 py-3 text-muted-foreground">
                {formatControlDate(cellString(row.expires_at))}
              </TableCell>
              <TableCell className="px-4 py-3 text-muted-foreground">
                {formatControlDate(cellString(row.created_at))}
              </TableCell>
            </>
          )}
        />
      )}
      {hasControlCapability(session, "platform.credentials.read") && (
        <SectionTable
          title="Credentials"
          headers={["Name", "Audience", "Status"]}
          query={credentials}
          empty="No credentials"
          cells={(row) => (
            <>
              <TableCell className="px-4 py-3">
                {cellText(row.name, "Session")}
                <p className="font-mono text-3xs text-muted-foreground">
                  {cellString(row.key_prefix) || cellString(row.id)}
                </p>
              </TableCell>
              <TableCell className="px-4 py-3">{cellText(row.audience)}</TableCell>
              <TableCell className="px-4 py-3">
                <StatusPill value={row.revoked_at ? "revoked" : "active"} />
              </TableCell>
            </>
          )}
        />
      )}
      {hasControlCapability(session, "platform.billing.read") && (
        <>
          <SectionTable
            title="Subscriptions"
            headers={["Reference", "Plan", "Status", "Period end"]}
            query={billing}
            empty="No subscriptions"
            cells={(row) => (
              <>
                <TableCell className="px-4 py-3 font-mono text-2xs">
                  {cellText(row.provider_subscription_code)}
                </TableCell>
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
          <SectionTable
            title="Checkouts"
            headers={["Reference", "Type", "Plan", "Status"]}
            query={checkouts}
            empty="No checkouts"
            cells={(row) => (
              <>
                <TableCell className="px-4 py-3 font-mono text-2xs">
                  {cellText(row.provider_reference)}
                </TableCell>
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
          <SectionTable
            title="Billing events"
            headers={["Event", "Status", "Reference", "Received"]}
            query={events}
            empty="No billing events"
            cells={(row) => (
              <>
                <TableCell className="px-4 py-3 font-mono text-2xs">
                  {cellText(row.event_type)}
                </TableCell>
                <TableCell className="px-4 py-3">
                  <StatusPill value={cellText(row.status)} />
                </TableCell>
                <TableCell className="px-4 py-3 font-mono text-3xs">
                  {cellText(row.provider_reference, "—")}
                </TableCell>
                <TableCell className="px-4 py-3 text-muted-foreground">
                  {formatControlDate(cellString(row.received_at))}
                </TableCell>
              </>
            )}
          />
        </>
      )}
      {hasControlCapability(session, "platform.audit.read") && (
        <SectionTable
          title="Audit"
          headers={["Action", "Reason", "Time"]}
          query={audit}
          empty="No audit events for this account"
          cells={(row) => (
            <>
              <TableCell className="px-4 py-3 font-semibold">{cellText(row.action)}</TableCell>
              <TableCell className="max-w-[320px] truncate px-4 py-3 text-muted-foreground">
                {cellText(row.reason)}
              </TableCell>
              <TableCell className="px-4 py-3 text-muted-foreground">
                {formatControlDate(cellString(row.created_at))}
              </TableCell>
            </>
          )}
        />
      )}

      {statusTarget && (
        <ActionDialog
          open={statusOpen}
          onOpenChangeComplete={statusComplete}
          onOpenChange={(open) => !open && setStatusTarget(null)}
          title={
            statusTarget === "closed"
              ? "Close account"
              : statusTarget === "suspended"
                ? "Suspend account"
                : "Reactivate account"
          }
          description={
            statusTarget === "closed"
              ? "Closing cancels active Paystack subscriptions, revokes credentials, and stops every monitor. This cannot be undone from the console."
              : statusTarget === "suspended"
                ? "Suspending revokes credentials and pauses every monitor until the account is reactivated."
                : "Reactivating restores workspace access for this tenant."
          }
          icon={statusTarget === "active" ? CheckCircle2 : Ban}
          tone={
            statusTarget === "closed"
              ? "danger"
              : statusTarget === "suspended"
                ? "danger"
                : "primary"
          }
          targetDetails={[
            { label: "Email", value: account.email || "Unverified" },
            { label: "Account ID", value: account.id, mono: true },
            { label: "Current status", value: account.status },
          ]}
          requireReason
          confirmLabel={
            statusTarget === "closed"
              ? "Close account"
              : statusTarget === "suspended"
                ? "Suspend"
                : "Reactivate"
          }
          isLoading={statusMutation.isPending}
          onConfirm={(reason) => statusMutation.mutate({ status: statusTarget, reason })}
        />
      )}
      {complimentaryOpen && (
        <ActionDialog
          open={complimentaryVisible}
          onOpenChangeComplete={complimentaryComplete}
          onOpenChange={(open) => !open && setComplimentaryOpen(false)}
          title={complimentary ? "Change complimentary plan" : "Grant complimentary plan"}
          description="The account pays nothing and keeps the plan's full limits. Any paid subscription is cancelled with Paystack, and pending checkouts are expired. Leave the end date empty for a perpetual grant."
          icon={Gift}
          tone="primary"
          targetDetails={[
            { label: "Email", value: account.email || "Unverified" },
            { label: "Account ID", value: account.id, mono: true },
            { label: "Current plan", value: account.plan },
          ]}
          requireReason
          confirmLabel={complimentary ? "Change plan" : "Grant plan"}
          isLoading={grantComplimentaryMutation.isPending}
          onConfirm={(reason) => grantComplimentaryMutation.mutate(reason)}
        >
          <div className="grid grid-cols-[minmax(0,1fr)] gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="complimentary-plan" className="text-xs font-medium">
                Plan
              </Label>
              <select
                id="complimentary-plan"
                className="h-9 w-full rounded-xl border border-input bg-transparent px-3 text-xs"
                value={complimentaryPlan}
                onChange={(event) => setComplimentaryPlan(event.target.value)}
              >
                <option value="starter">Starter</option>
                <option value="pro">Pro</option>
                <option value="business">Business</option>
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="complimentary-expiry" className="text-xs font-medium">
                Ends on (optional)
              </Label>
              <Input
                id="complimentary-expiry"
                type="date"
                className="rounded-lg text-xs"
                value={complimentaryExpiry}
                onChange={(event) => setComplimentaryExpiry(event.target.value)}
              />
            </div>
          </div>
        </ActionDialog>
      )}
      {revokeComplimentaryOpen && (
        <ActionDialog
          open={revokeComplimentaryVisible}
          onOpenChangeComplete={revokeComplimentaryComplete}
          onOpenChange={(open) => !open && setRevokeComplimentaryOpen(false)}
          title="End complimentary plan"
          description="The account drops back to whatever subscription it still has, or to free. Monitors over the free limit stay stored but the account cannot create more."
          icon={Gift}
          tone="danger"
          targetDetails={[
            { label: "Email", value: account.email || "Unverified" },
            { label: "Account ID", value: account.id, mono: true },
            { label: "Complimentary plan", value: complimentary ?? "—" },
          ]}
          requireReason
          confirmLabel="End it"
          isLoading={revokeComplimentaryMutation.isPending}
          onConfirm={(reason) => revokeComplimentaryMutation.mutate(reason)}
        />
      )}
      {reconcileOpen && (
        <ActionDialog
          open={reconcileVisible}
          onOpenChangeComplete={reconcileComplete}
          onOpenChange={(open) => !open && setReconcileOpen(false)}
          title="Reconcile billing"
          description="Fetch Paystack subscription and checkout state and repair local billing records."
          icon={CircleDollarSign}
          tone="primary"
          targetDetails={[
            { label: "Email", value: account.email || "Unverified" },
            { label: "Account ID", value: account.id, mono: true },
          ]}
          requireReason
          confirmLabel="Reconcile"
          isLoading={reconcileMutation.isPending}
          onConfirm={(reason) => reconcileMutation.mutate(reason)}
        />
      )}
    </div>
  );
}

function SectionTable({
  title,
  headers,
  query,
  empty,
  cells,
}: {
  title: string;
  headers: string[];
  query: ReturnType<typeof useControlPage>;
  empty: string;
  cells: (row: ControlRow) => ReactNode;
}) {
  const rows = query.rows;
  return (
    <div className="space-y-3">
      <h2 className="text-sm font-semibold">{title}</h2>
      {rows.length ? (
        <>
          <ControlTable headers={[...headers, "Actions"]}>
            {rows.map((row) => (
              <TableRow
                key={
                  cellString(row.id) ||
                  cellString(row.period) ||
                  `${cellText(row.action)}-${cellText(row.created_at)}`
                }
              >
                {cells(row)}
                <TableCell className="px-4 py-3 text-right">
                  <ControlRowActions row={row} />
                </TableCell>
              </TableRow>
            ))}
          </ControlTable>
          <ControlPager query={query} />
        </>
      ) : (
        <ControlEmpty label={empty} />
      )}
    </div>
  );
}
