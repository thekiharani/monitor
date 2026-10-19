import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  AtSign,
  Building2,
  Check,
  Mail,
  ShieldCheck,
  TriangleAlert,
  UserRound,
} from "lucide-react";
import { toast } from "sonner";
import { errorToast } from "@/lib/toast";
import { personInitials, personName } from "@/lib/identity";
import type { Account as AccountType } from "@/types";
import { APIError, api, endpoints, queryKeys } from "@/api";
import { PageHeader } from "@/components/layout/Header";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
import { ThemeDropdown } from "@/components/layout/ThemeDropdown";
import { TimezoneCombobox } from "@/components/account/TimezoneCombobox";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type ProfileResponse = { data: { account: AccountType } };

function useProfileRefresh() {
  const queryClient = useQueryClient();
  return (updated: ProfileResponse) => {
    queryClient.setQueryData(queryKeys.me, updated);
    void queryClient.invalidateQueries({ queryKey: queryKeys.accounts });
    void queryClient.invalidateQueries({ queryKey: queryKeys.members });
  };
}

function ProfileForm({ account }: { account: AccountType }) {
  const refresh = useProfileRefresh();
  const [displayName, setDisplayName] = useState(account.display_name);
  const [timezone, setTimezone] = useState(account.timezone);

  const save = useMutation({
    mutationFn: () =>
      api<ProfileResponse>(endpoints.profile(), {
        method: "PATCH",
        body: JSON.stringify({ display_name: displayName, timezone }),
      }),
    onSuccess: (updated) => {
      refresh(updated);
      toast.success("Profile saved.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't save your profile."),
  });

  const dirty = displayName !== account.display_name || timezone !== account.timezone;

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <div className="flex items-center gap-2">
          <UserRound className="size-4 text-primary" />
          <CardTitle className="text-sm font-semibold">Your profile</CardTitle>
        </div>
      </CardHeader>

      <CardContent className="space-y-5 p-6 pt-2">
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
          <span className="grid size-14 shrink-0 place-items-center rounded-xl bg-brand-soft text-xl font-semibold text-primary shadow-xs ring-1 ring-primary/20">
            {personInitials(account)}
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <p className="truncate text-base font-semibold text-foreground">
              {personName(account)}
            </p>
            <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Mail className="size-3 shrink-0" />
              <span className="truncate">{account.email}</span>
            </p>
          </div>
          <Badge
            variant="outline"
            className="w-fit border-success/30 bg-success/10 text-xs font-medium text-success"
          >
            <Check className="mr-1 size-3" />
            Active Session
          </Badge>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="display-name" className="text-xs font-medium">
            Display name
          </Label>
          <Input
            id="display-name"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
            placeholder="How your teammates see you"
            maxLength={128}
            autoComplete="name"
            className="rounded-xl"
          />
          <p className="text-xs text-muted-foreground">
            Shown on the team list and in the account menu. Leave it empty to be listed by your
            email address.
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="timezone" className="text-xs font-medium">
            Time zone
          </Label>
          <TimezoneCombobox id="timezone" value={timezone} onChange={setTimezone} />
          <p className="text-xs text-muted-foreground">
            Dates and times across the dashboard are shown in this zone. Daily uptime is counted in
            whole UTC days, so the daily totals do not shift.
          </p>
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-border pt-4">
          {dirty && (
            <Button
              variant="ghost"
              size="lg"
              onClick={() => {
                setDisplayName(account.display_name);
                setTimezone(account.timezone);
              }}
              className="rounded-xl text-xs"
            >
              <ArrowLeft className="size-3.5" />
              Discard
            </Button>
          )}
          <Button
            size="lg"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate()}
            className="rounded-xl text-xs font-medium"
          >
            {save.isPending ? "Saving…" : "Save profile"}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function EmailForm({ account }: { account: AccountType }) {
  const refresh = useProfileRefresh();
  const [newEmail, setNewEmail] = useState("");
  const [challenge, setChallenge] = useState("");
  const [code, setCode] = useState("");

  const request = useMutation({
    mutationFn: () =>
      api<{ challenge_id: string }>(endpoints.changeEmail(), {
        method: "POST",
        body: JSON.stringify({ email: newEmail.trim() }),
      }),
    onSuccess: (data) => {
      setChallenge(data.challenge_id);
      toast.success(`We sent a 6-digit code to ${newEmail.trim()}.`);
    },
    onError: (err: Error) => errorToast(err, "We couldn't send a code to that address."),
  });

  const verify = useMutation({
    mutationFn: () =>
      api<ProfileResponse>(endpoints.verifyEmailChange(), {
        method: "POST",
        body: JSON.stringify({
          challenge_id: challenge,
          email: newEmail.trim(),
          code: code.trim(),
        }),
      }),
    onSuccess: (updated) => {
      refresh(updated);
      setChallenge("");
      setNewEmail("");
      setCode("");
      toast.success("Your sign-in address has been changed.");
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status === 401) {
        toast.error("That code is wrong or has expired. Request a new one.");
        return;
      }
      errorToast(err, "We couldn't change your email address.");
    },
  });

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <div className="flex items-center gap-2">
          <AtSign className="size-4 text-primary" />
          <CardTitle className="text-sm font-semibold">Sign-in email</CardTitle>
        </div>
      </CardHeader>

      <CardContent className="space-y-4 p-6 pt-2">
        <p className="text-xs leading-relaxed text-muted-foreground">
          You sign in with <span className="font-semibold text-foreground">{account.email}</span>.
          To move to another address we send it a 6-digit code; nothing changes until you enter that
          code. Every other session is signed out afterwards.
        </p>

        {challenge ? (
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              verify.mutate();
            }}
          >
            <Label htmlFor="email-code" className="text-xs font-medium">
              Code sent to {newEmail.trim()}
            </Label>
            <Input
              id="email-code"
              value={code}
              onChange={(event) => setCode(event.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              placeholder="000000"
              className="rounded-xl text-center font-mono text-lg tracking-[0.4em]"
            />
            <div className="flex flex-wrap gap-2">
              <Button
                type="submit"
                size="lg"
                disabled={code.trim().length !== 6 || verify.isPending}
                className="rounded-lg text-xs font-medium"
              >
                {verify.isPending ? "Confirming…" : "Confirm new address"}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="lg"
                onClick={() => {
                  setChallenge("");
                  setCode("");
                }}
                className="rounded-xl text-xs"
              >
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              request.mutate();
            }}
          >
            <Label htmlFor="new-email" className="text-xs font-medium">
              New email address
            </Label>
            <Input
              id="new-email"
              type="email"
              value={newEmail}
              onChange={(event) => setNewEmail(event.target.value)}
              placeholder="you@company.com"
              autoComplete="email"
              className="rounded-xl"
            />
            <Button
              type="submit"
              size="lg"
              disabled={!newEmail.trim() || request.isPending}
              className="rounded-lg text-xs font-medium"
            >
              {request.isPending ? "Sending code…" : "Send confirmation code"}
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}

function WorkspaceForm({ account }: { account: AccountType }) {
  const refresh = useProfileRefresh();
  const [name, setName] = useState(account.name);
  const canManage = account.role === "owner" || account.role === "admin";

  const save = useMutation({
    mutationFn: () =>
      api<ProfileResponse>(endpoints.workspace(), {
        method: "PATCH",
        body: JSON.stringify({ name }),
      }),
    onSuccess: (updated) => {
      refresh(updated);
      toast.success("Workspace name saved.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't rename the workspace."),
  });

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <div className="flex items-center gap-2">
          <Building2 className="size-4 text-primary" />
          <CardTitle className="text-sm font-semibold">Workspace</CardTitle>
        </div>
      </CardHeader>

      <CardContent className="space-y-4 p-6 pt-2">
        <div className="space-y-1.5">
          <Label htmlFor="workspace-name" className="text-xs font-medium">
            Workspace name
          </Label>
          <Input
            id="workspace-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Untitled workspace"
            maxLength={128}
            disabled={!canManage}
            className="rounded-xl"
          />
          <p className="text-xs text-muted-foreground">
            {canManage
              ? "Everyone on this workspace sees this name in the switcher and the sidebar."
              : "Only an owner or admin can rename this workspace."}
          </p>
        </div>

        <div className="grid grid-cols-2 gap-4 border-t border-border pt-4 text-xs">
          <div>
            <span className="text-muted-foreground">Account ID</span>
            <p className="mt-0.5 truncate font-mono text-xs font-medium text-foreground">
              {account.id}
            </p>
          </div>
          <div>
            <span className="text-muted-foreground">Plan</span>
            <p className="mt-0.5 font-semibold capitalize text-primary">{account.plan} Plan</p>
          </div>
        </div>

        {canManage && (
          <div className="flex justify-end">
            <Button
              size="lg"
              disabled={name === account.name || save.isPending}
              onClick={() => save.mutate()}
              className="rounded-xl text-xs font-medium"
            >
              {save.isPending ? "Saving…" : "Save workspace name"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function Account() {
  const [confirm, setConfirm] = useState("");

  const accountQuery = useQuery({
    queryKey: queryKeys.me,
    queryFn: () => api<ProfileResponse>(endpoints.me),
  });

  const closeMutation = useMutation({
    mutationFn: () =>
      api<void>(endpoints.closeAccount(), {
        method: "POST",
        body: JSON.stringify({ confirm: confirm.trim() }),
      }),
    onSuccess: () => {
      toast.success("Account closed.");
      window.location.assign("/");
    },
    onError: (err: Error) => errorToast(err, "We couldn't close the account."),
  });

  if (accountQuery.isLoading) {
    return <PageSkeleton />;
  }

  const account = accountQuery.data?.data.account;
  if (!account) return null;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Account"
        description="Your profile, the email you sign in with, and this workspace."
        breadcrumbs={[{ label: "Account" }]}
      />

      <div className="grid grid-cols-[minmax(0,1fr)] gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="space-y-6">
          <ProfileForm
            key={`profile-${account.display_name}-${account.timezone}`}
            account={account}
          />
          <EmailForm account={account} />
          <WorkspaceForm key={`workspace-${account.name}`} account={account} />

          <Card>
            <CardHeader className="p-6 pb-3">
              <CardTitle className="text-sm font-semibold">Appearance</CardTitle>
            </CardHeader>
            <CardContent className="flex items-center justify-between p-6 pt-2">
              <div className="space-y-0.5">
                <p className="text-xs font-medium text-foreground">Theme Preference</p>
                <p className="text-xs text-muted-foreground">
                  Switch between Light, Dark, or automatic system sync. This one is remembered by
                  this browser, not by your account.
                </p>
              </div>
              <ThemeDropdown />
            </CardContent>
          </Card>
        </div>

        <div className="space-y-4">
          <Card>
            <CardHeader className="p-5 pb-2">
              <div className="flex items-center gap-2 text-primary">
                <ShieldCheck className="size-5" />
                <CardTitle className="text-sm font-semibold text-foreground">
                  Security &amp; Privacy
                </CardTitle>
              </div>
            </CardHeader>
            <CardContent className="space-y-3 p-5 pt-1 text-xs leading-relaxed text-muted-foreground">
              <p>
                Noria Monitor uses passwordless authentication via one-time cryptographically secure
                email tokens.
              </p>
              <p>
                All sessions are protected with HTTP-only cookies and double-submit CSRF tokens.
              </p>
              <p>
                Changing your sign-in address signs out every other session and tells the old
                address it happened.
              </p>
            </CardContent>
          </Card>

          <Card className="ring-destructive/30">
            <CardHeader className="p-5 pb-2">
              <div className="flex items-center gap-2 text-destructive">
                <TriangleAlert className="size-5" />
                <CardTitle className="text-sm font-semibold text-foreground">
                  Close account
                </CardTitle>
              </div>
            </CardHeader>
            <CardContent className="space-y-3 p-5 pt-1">
              <p className="text-xs leading-relaxed text-muted-foreground">
                Closing stops every monitor and status page on this account immediately, revokes all
                API keys and sessions, and removes your teammates. It cannot be undone, and an
                account cannot be reopened. Cancel your subscription before closing.
              </p>
              <Label htmlFor="close-confirm" className="text-xs font-medium">
                Type <span className="font-mono">{account.email}</span> to confirm
              </Label>
              <Input
                id="close-confirm"
                value={confirm}
                onChange={(event) => setConfirm(event.target.value)}
                placeholder={account.email}
                autoComplete="off"
                className="rounded-xl font-mono text-xs"
              />
              <Button
                variant="ghost"
                disabled={confirm.trim() !== account.email || closeMutation.isPending}
                onClick={() => closeMutation.mutate()}
                className="h-9 w-full rounded-xl bg-destructive/10 text-xs font-medium text-destructive hover:bg-destructive/20"
              >
                {closeMutation.isPending ? "Closing…" : "Close this account permanently"}
              </Button>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
