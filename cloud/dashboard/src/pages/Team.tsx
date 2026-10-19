import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link as RouterLink } from "react-router-dom";
import { Copy, Crown, Mail, ShieldCheck, Trash2, UserPlus, Users } from "lucide-react";
import { toast } from "sonner";
import { errorToast } from "@/lib/toast";
import { personInitials, personName } from "@/lib/identity";
import type { Member, MemberInvite, TeamResponse, TeamRole } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { PageHeader } from "@/components/layout/Header";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
import { ActionDialog } from "@/components/common/ActionDialog";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RowActions, copyRowValue } from "@/components/ui/row-actions";
import {
  Slideover,
  SlideoverBody,
  SlideoverContent,
  SlideoverHeader,
  SlideoverFooter,
  useSlideoverState,
} from "@/components/ui/slideover";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { usePageMetadata } from "@/lib/metadata";
import { useDateFormat } from "@/context/timezone-context";

const ROLE_COPY: Record<TeamRole, string> = {
  owner: "Full access, including billing and the team itself.",
  admin: "Manages monitors, status pages, keys and teammates. No billing.",
  member: "Creates and manages monitors and status pages.",
};

export function Team() {
  const { formatDate } = useDateFormat();
  usePageMetadata({
    title: "Team",
    description: "Invite teammates into this workspace and manage what they can do.",
  });

  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"admin" | "member">("member");
  const [pendingRemove, setPendingRemove, removeOpen, removeComplete] =
    useSlideoverState<Member | null>(null);
  const [inviteOpen, setInviteOpen] = useState(false);

  const teamQuery = useQuery({
    queryKey: queryKeys.members,
    queryFn: () => api<{ data: TeamResponse }>(endpoints.members()),
  });

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.members });
  };

  const inviteMutation = useMutation({
    mutationFn: () =>
      api<{ data: MemberInvite }>(endpoints.memberInvites(), {
        method: "POST",
        body: JSON.stringify({ email: email.trim(), role }),
      }),
    onSuccess: () => {
      setInviteOpen(false);
      invalidate();
      toast.success("Invitation sent.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't send that invitation."),
  });

  const resendMutation = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.memberInviteResend(id), { method: "POST" }),
    onSuccess: () => {
      invalidate();
      toast.success("Invitation sent again.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't resend that invitation."),
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.memberInvite(id), { method: "DELETE" }),
    onSuccess: () => {
      invalidate();
      toast.success("Invitation revoked.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't revoke that invitation."),
  });

  const roleMutation = useMutation({
    mutationFn: ({ id, next }: { id: string; next: TeamRole }) =>
      api<void>(endpoints.member(id), { method: "PATCH", body: JSON.stringify({ role: next }) }),
    onSuccess: () => {
      invalidate();
      toast.success("Role updated.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't change that role."),
  });

  const removeMutation = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.member(id), { method: "DELETE" }),
    onSuccess: () => {
      setPendingRemove(null);
      invalidate();
      toast.success("Teammate removed.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't remove that teammate."),
  });

  if (teamQuery.isLoading) return <PageSkeleton />;

  const team = teamQuery.data?.data;
  if (!team) return null;

  const seatsLeft = Math.max(0, team.limit - team.seats_taken);
  const isOwner = team.your_role === "owner";

  return (
    <div className="space-y-6">
      <PageHeader
        title="Team"
        description="Invite teammates into this workspace and control what they can do."
        breadcrumbs={[{ label: "Team" }]}
        action={
          team.can_manage ? (
            <Button onClick={() => setInviteOpen(true)}>
              <UserPlus />
              Invite teammate
            </Button>
          ) : undefined
        }
      />

      {team.can_manage && (
        <Slideover
          open={inviteOpen}
          onOpenChange={(open) => {
            if (inviteMutation.isPending) return;
            setInviteOpen(open);
          }}
          onOpenChangeComplete={(open) => {
            if (!open) {
              setEmail("");
              setRole("member");
            }
          }}
        >
          <SlideoverContent size="md">
            <SlideoverHeader
              title="Invite a teammate"
              description={`${team.seats_taken} of ${team.limit} seats used · ${seatsLeft} remaining`}
              icon={UserPlus}
              showCloseButton={!inviteMutation.isPending}
            />
            <form
              className="flex min-h-0 flex-1 flex-col"
              onSubmit={(event) => {
                event.preventDefault();
                if (email.trim() && seatsLeft > 0 && !inviteMutation.isPending)
                  inviteMutation.mutate();
              }}
            >
              <SlideoverBody className="space-y-5">
                <div className="space-y-2">
                  <Label htmlFor="invite-email">Email address</Label>
                  <Input
                    id="invite-email"
                    type="email"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    placeholder="teammate@yourcompany.com"
                    className="text-sm"
                    autoComplete="off"
                    disabled={seatsLeft === 0}
                    aria-label="Teammate email"
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label id="invite-role-label">Workspace role</Label>
                  <Select
                    value={role}
                    onValueChange={(value) => setRole((value ?? "member") as "admin" | "member")}
                    disabled={seatsLeft === 0}
                  >
                    <SelectTrigger size="xl" aria-labelledby="invite-role-label" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="member">Member</SelectItem>
                      <SelectItem value="admin">Admin</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-sm leading-relaxed text-muted-foreground">{ROLE_COPY[role]}</p>
                </div>
                {seatsLeft === 0 && (
                  <p className="text-sm text-muted-foreground">
                    Every seat is taken.{" "}
                    <RouterLink to="/app/billing" className="font-medium text-primary">
                      Upgrade your plan
                    </RouterLink>{" "}
                    to invite more teammates.
                  </p>
                )}
              </SlideoverBody>
              <SlideoverFooter>
                <Button
                  type="button"
                  variant="outline"
                  disabled={inviteMutation.isPending}
                  onClick={() => setInviteOpen(false)}
                >
                  Cancel
                </Button>
                <Button
                  size="xl"
                  type="submit"
                  disabled={seatsLeft === 0 || !email.trim() || inviteMutation.isPending}
                >
                  <UserPlus className="mr-1.5 size-3.5" />
                  {inviteMutation.isPending ? "Sending…" : "Send invite"}
                </Button>
              </SlideoverFooter>
            </form>
          </SlideoverContent>
        </Slideover>
      )}

      <Card className="gap-0 py-0">
        <CardHeader className="p-6 pb-3">
          <CardTitle className="text-sm font-semibold">Members</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <ul className="divide-y divide-border">
            {team.members.map((member) => (
              <li key={member.principal_id} className="flex flex-wrap items-center gap-3 px-6 py-4">
                <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-brand-soft text-sm font-semibold text-primary">
                  {personInitials(member)}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-foreground">
                    {personName(member) || "Unknown address"}
                    {member.is_you && (
                      <span className="ml-2 text-2xs font-normal text-muted-foreground">you</span>
                    )}
                  </p>
                  <p className="truncate text-2xs text-muted-foreground">
                    {member.display_name?.trim() ? `${member.email} · ` : ""}
                    Joined {formatDate(member.joined_at)}
                  </p>
                </div>

                {member.role === "owner" ? (
                  <Badge
                    variant="outline"
                    className="border-primary/30 bg-brand-soft text-xs font-medium text-primary"
                  >
                    <Crown className="mr-1 size-3" />
                    Owner
                  </Badge>
                ) : isOwner ? (
                  <Select
                    value={member.role}
                    onValueChange={(value) =>
                      roleMutation.mutate({
                        id: member.principal_id,
                        next: (value ?? member.role) as TeamRole,
                      })
                    }
                  >
                    <SelectTrigger
                      size="sm"
                      aria-label={`Role for ${member.email}`}
                      className="font-semibold"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="member">Member</SelectItem>
                      <SelectItem value="admin">Admin</SelectItem>
                    </SelectContent>
                  </Select>
                ) : (
                  <Badge variant="outline" className="text-xs font-medium capitalize">
                    <ShieldCheck className="mr-1 size-3" />
                    {member.role}
                  </Badge>
                )}

                <RowActions
                  label={`Actions for ${member.email}`}
                  items={[
                    {
                      key: "email",
                      label: "Copy email",
                      icon: Copy,
                      onClick: () => {
                        void copyRowValue(member.email, "Email");
                      },
                    },
                    {
                      key: "copy",
                      label: "Copy reference",
                      icon: Copy,
                      onClick: () => {
                        void copyRowValue(member.principal_id);
                      },
                    },
                    ...(member.role !== "owner" && (team.can_manage || member.is_you)
                      ? [
                          {
                            key: "remove",
                            label: member.is_you ? "Leave workspace" : "Remove teammate",
                            icon: Trash2,
                            variant: "destructive" as const,
                            separator: true,
                            onClick: () => setPendingRemove(member),
                          },
                        ]
                      : []),
                  ]}
                />
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      {team.invites.length > 0 && (
        <Card className="gap-0 py-0">
          <CardHeader className="p-6 pb-3">
            <CardTitle className="text-sm font-semibold">Pending invitations</CardTitle>
            <p className="text-xs text-muted-foreground">
              A pending invitation holds a seat until it is accepted or revoked.
            </p>
          </CardHeader>
          <CardContent className="p-0">
            <ul className="divide-y divide-border">
              {team.invites.map((invite) => (
                <li key={invite.id} className="flex flex-wrap items-center gap-3 px-6 py-4">
                  <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-muted text-muted-foreground">
                    <Mail className="size-4" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-foreground">{invite.email}</p>
                    <p className="text-2xs text-muted-foreground">
                      Invited as {invite.role} · expires {formatDate(invite.expires_at)}
                    </p>
                  </div>
                  {team.can_manage && (
                    <RowActions
                      label={`Actions for invitation ${invite.email}`}
                      items={[
                        {
                          key: "resend",
                          label: "Resend invitation",
                          icon: Mail,
                          disabled: resendMutation.isPending,
                          onClick: () => resendMutation.mutate(invite.id),
                        },
                        {
                          key: "copy",
                          label: "Copy email",
                          icon: Copy,
                          onClick: () => {
                            void copyRowValue(invite.email, "Email");
                          },
                        },
                        {
                          key: "revoke",
                          label: "Revoke invitation",
                          icon: Trash2,
                          variant: "destructive",
                          separator: true,
                          disabled: revokeMutation.isPending,
                          onClick: () => revokeMutation.mutate(invite.id),
                        },
                      ]}
                    />
                  )}
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <Card className="gap-0 py-0">
        <CardContent className="flex items-start gap-4 p-6">
          <span className="grid size-10 place-items-center rounded-lg bg-muted text-muted-foreground">
            <Users className="size-5" />
          </span>
          <div className="space-y-2 text-xs">
            <h3 className="text-sm font-semibold text-foreground">What each role can do</h3>
            <ul className="space-y-1 text-muted-foreground">
              {(Object.keys(ROLE_COPY) as TeamRole[]).map((key) => (
                <li key={key}>
                  <span className="font-semibold capitalize text-foreground">{key}</span> —{" "}
                  {ROLE_COPY[key]}
                </li>
              ))}
            </ul>
          </div>
        </CardContent>
      </Card>

      <ActionDialog
        open={removeOpen}
        onOpenChangeComplete={removeComplete}
        onOpenChange={(open) => !open && setPendingRemove(null)}
        title={
          pendingRemove?.is_you ? "Leave this workspace?" : `Remove ${pendingRemove?.email ?? ""}?`
        }
        description={
          pendingRemove?.is_you
            ? "You will lose access to this workspace and its monitors immediately."
            : "They lose access to this workspace immediately, and any sessions or API keys they hold for it are revoked."
        }
        icon={Trash2}
        tone="danger"
        confirmLabel={pendingRemove?.is_you ? "Leave workspace" : "Remove teammate"}
        isLoading={removeMutation.isPending}
        onConfirm={() => {
          if (pendingRemove) removeMutation.mutate(pendingRemove.principal_id);
        }}
      />
    </div>
  );
}
