import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertTriangle,
  BellRing,
  CalendarClock,
  Check,
  Copy,
  Hash,
  Mail,
  Pencil,
  Plus,
  Send,
  Trash2,
  Webhook,
} from "lucide-react";
import type {
  Account,
  AlertChannel,
  AlertChannelList,
  ChannelDelivery,
  ChannelInput,
  ChannelKind,
  MaintenanceInput,
  MaintenanceWindow,
} from "@/types";
import { APIError, api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import { formatRelative, useMonitors, useNow } from "@/lib/monitors";
import { useDateFormat } from "@/context/timezone-context";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/components/layout/Header";
import { ActionDialog } from "@/components/common/ActionDialog";
import { RowActions } from "@/components/ui/row-actions";
import { EmptyState } from "@/components/common/EmptyState";
import { TableSkeleton } from "@/components/common/SkeletonLoaders";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Slideover,
  SlideoverBody,
  SlideoverContent,
  SlideoverFooter,
  SlideoverHeader,
  useSlideoverState,
} from "@/components/ui/slideover";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

const kindMeta: Record<ChannelKind, { label: string; icon: typeof Mail }> = {
  email: { label: "Email", icon: Mail },
  telegram: { label: "Telegram", icon: Send },
  slack: { label: "Slack", icon: Hash },
  webhook: { label: "Webhook", icon: Webhook },
};

const eventLabels: Record<ChannelDelivery["event"], string> = {
  down: "Down",
  up: "Recovered",
  certificate: "Certificate",
  test: "Test",
};

export function channelDestination(channel: AlertChannel): string {
  switch (channel.kind) {
    case "email":
      return (channel.config.emails ?? []).join(", ");
    case "telegram":
      return `Chat ${channel.config.chat_id ?? ""}${channel.config.topic_id ? ` · topic ${channel.config.topic_id}` : ""}`;
    case "slack":
      return channel.config.webhook_url ?? "";
    case "webhook":
      return channel.config.url ?? "";
    default:
      return "";
  }
}

function useCanManage() {
  const me = useQuery({
    queryKey: queryKeys.me,
    queryFn: () => api<{ data: { account: Account } }>(endpoints.me),
  });
  const role = me.data?.data.account.role;
  return role === "owner" || role === "admin";
}

function MonitorPicker({
  selected,
  onChange,
  label,
}: {
  selected: string[];
  onChange: (ids: string[]) => void;
  label: string;
}) {
  const monitors = useMonitors().data?.data ?? [];
  if (monitors.length === 0) {
    return <p className="text-xs text-muted-foreground">You have no monitors yet.</p>;
  }
  return (
    <div role="group" aria-label={label} className="flex flex-wrap gap-2">
      {monitors.map((monitor) => {
        const on = selected.includes(monitor.id);
        return (
          <button
            key={monitor.id}
            type="button"
            aria-pressed={on}
            onClick={() =>
              onChange(on ? selected.filter((id) => id !== monitor.id) : [...selected, monitor.id])
            }
            className={cn(
              "inline-flex h-8 items-center gap-1.5 rounded-lg border px-3 text-xs transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
              on ? "border-primary/40 bg-brand-soft text-primary" : "border-input hover:bg-muted",
            )}
          >
            {on && <Check className="size-3.5" />}
            {monitor.name}
          </button>
        );
      })}
    </div>
  );
}

export function ChannelForm({
  channel,
  telegramAvailable,
  telegramBot,
  onSaved,
}: {
  channel?: AlertChannel;
  telegramAvailable: boolean;
  telegramBot: string;
  onSaved: (saved: AlertChannel) => void;
}) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<ChannelKind>(channel?.kind ?? "email");
  const [name, setName] = useState(channel?.name ?? "");
  const [emails, setEmails] = useState((channel?.config.emails ?? []).join(", "));
  const [chatId, setChatId] = useState(channel?.config.chat_id ?? "");
  const [topicId, setTopicId] = useState(
    channel?.config.topic_id ? String(channel.config.topic_id) : "",
  );
  const [slackUrl, setSlackUrl] = useState("");
  const [hookUrl, setHookUrl] = useState(channel?.config.url ?? "");
  const [allMonitors, setAllMonitors] = useState(channel?.all_monitors ?? true);
  const [monitorIds, setMonitorIds] = useState<string[]>(channel?.monitor_ids ?? []);
  const [error, setError] = useState("");

  const save = useMutation({
    mutationFn: (input: ChannelInput) =>
      channel
        ? api<{ data: AlertChannel }>(endpoints.alertChannel(channel.id), {
            method: "PATCH",
            body: JSON.stringify(input),
          })
        : api<{ data: AlertChannel }>(endpoints.alertChannels(), {
            method: "POST",
            body: JSON.stringify(input),
          }),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.alertChannels });
      toast.success(channel ? "Channel saved." : "Channel added. Send a test to check it.");
      onSaved(result.data);
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status < 500) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't save the channel.");
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    const input: ChannelInput = {
      name: name.trim(),
      all_monitors: allMonitors,
      monitor_ids: allMonitors ? [] : monitorIds,
    };
    if (!channel) input.kind = kind;
    if (kind === "email") input.emails = emails.split(/[\s,;]+/).filter(Boolean);
    if (kind === "telegram") {
      input.chat_id = chatId.trim();
      input.topic_id = topicId.trim() ? Number(topicId) : 0;
    }
    if (kind === "slack" && (slackUrl.trim() || !channel)) input.webhook_url = slackUrl.trim();
    if (kind === "webhook") input.url = hookUrl.trim();
    if (!allMonitors && monitorIds.length === 0) {
      setError("Pick at least one monitor, or send alerts for every monitor.");
      return;
    }
    save.mutate(input);
  };

  return (
    <form id="channel-form" onSubmit={submit} className="flex min-h-0 flex-1 flex-col" noValidate>
      <SlideoverBody>
        <FieldGroup>
          {error && (
            <Alert variant="destructive">
              <AlertTriangle />
              <AlertTitle>Please check the channel</AlertTitle>
              <AlertDescription>
                {error}{" "}
                {error.toLowerCase().includes("plan") && (
                  <Link to="/app/billing">Upgrade your plan</Link>
                )}
              </AlertDescription>
            </Alert>
          )}
          <Field>
            <FieldLabel id="channel-kind-label" className="text-xs font-medium">
              Send alerts by
            </FieldLabel>
            <ToggleGroup
              aria-labelledby="channel-kind-label"
              variant="outline"
              className="flex-wrap"
              value={[kind]}
              disabled={Boolean(channel)}
              onValueChange={(value) => value[0] && setKind(value[0] as ChannelKind)}
            >
              {(Object.keys(kindMeta) as ChannelKind[]).map((option) => {
                const Icon = kindMeta[option].icon;
                return (
                  <ToggleGroupItem
                    key={option}
                    value={option}
                    disabled={option === "telegram" && !telegramAvailable}
                    className="gap-1.5 px-3 text-xs aria-pressed:border-primary/40 aria-pressed:bg-brand-soft aria-pressed:text-primary"
                  >
                    <Icon />
                    {kindMeta[option].label}
                  </ToggleGroupItem>
                );
              })}
            </ToggleGroup>
            {!telegramAvailable && (
              <FieldDescription className="text-xs">
                Telegram alerts are not switched on for this server yet.
              </FieldDescription>
            )}
          </Field>
          <Field>
            <FieldLabel htmlFor="channel-name" className="text-xs font-medium">
              Name
            </FieldLabel>
            <Input
              id="channel-name"
              value={name}
              maxLength={128}
              placeholder="On-call"
              onChange={(event) => setName(event.target.value)}
            />
          </Field>

          {kind === "email" && (
            <Field>
              <FieldLabel htmlFor="channel-emails" className="text-xs font-medium">
                Email addresses
              </FieldLabel>
              <Textarea
                id="channel-emails"
                rows={3}
                value={emails}
                placeholder="ops@example.com, oncall@example.com"
                onChange={(event) => setEmails(event.target.value)}
              />
              <FieldDescription className="text-xs">
                Up to 10, separated by commas or new lines.
              </FieldDescription>
            </Field>
          )}

          {kind === "telegram" && (
            <>
              <ol className="list-decimal space-y-1 pl-5 text-xs leading-relaxed text-muted-foreground">
                <li>
                  Add <span className="font-mono text-foreground">@{telegramBot || "our bot"}</span>{" "}
                  to your group or channel. To pin outages, make it an admin that can pin messages.
                </li>
                <li>
                  Send any message in the chat, then paste the chat id below (groups start with
                  -100).
                </li>
              </ol>
              <Field>
                <FieldLabel htmlFor="channel-chat" className="text-xs font-medium">
                  Chat id
                </FieldLabel>
                <Input
                  id="channel-chat"
                  className="font-mono"
                  value={chatId}
                  placeholder="-1001234567890"
                  onChange={(event) => setChatId(event.target.value)}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="channel-topic" className="text-xs font-medium">
                  Topic id (optional)
                </FieldLabel>
                <Input
                  id="channel-topic"
                  inputMode="numeric"
                  className="font-mono"
                  value={topicId}
                  onChange={(event) => setTopicId(event.target.value)}
                />
                <FieldDescription className="text-xs">
                  For forum groups, to post alerts in one topic.
                </FieldDescription>
              </Field>
            </>
          )}

          {kind === "slack" && (
            <Field>
              <FieldLabel htmlFor="channel-slack" className="text-xs font-medium">
                Incoming webhook URL
              </FieldLabel>
              <Input
                id="channel-slack"
                className="font-mono text-xs"
                value={slackUrl}
                placeholder={
                  channel
                    ? `${channelDestination(channel)} (leave blank to keep)`
                    : "https://hooks.slack.com/services/…"
                }
                onChange={(event) => setSlackUrl(event.target.value)}
              />
              <FieldDescription className="text-xs">
                Create one under{" "}
                <a href="https://api.slack.com/messaging/webhooks" target="_blank" rel="noreferrer">
                  Slack incoming webhooks
                </a>{" "}
                and pick the channel alerts should land in.
              </FieldDescription>
            </Field>
          )}

          {kind === "webhook" && (
            <Field>
              <FieldLabel htmlFor="channel-url" className="text-xs font-medium">
                Endpoint URL
              </FieldLabel>
              <Input
                id="channel-url"
                className="font-mono text-xs"
                value={hookUrl}
                placeholder="https://example.com/hooks/noria"
                onChange={(event) => setHookUrl(event.target.value)}
              />
              <FieldDescription className="text-xs">
                We POST JSON and sign it with an{" "}
                <span className="font-mono">X-Noria-Signature: t=…,v1=…</span> header: an
                HMAC-SHA256 of <span className="font-mono">{"<t>.<body>"}</span> using the signing
                secret shown once after you save.
              </FieldDescription>
            </Field>
          )}

          <Field>
            <Field orientation="horizontal">
              <Switch id="channel-all" checked={allMonitors} onCheckedChange={setAllMonitors} />
              <FieldLabel htmlFor="channel-all" className="text-xs font-normal">
                Alert for every monitor, including ones added later
              </FieldLabel>
            </Field>
            {!allMonitors && (
              <MonitorPicker
                label="Monitors this channel hears about"
                selected={monitorIds}
                onChange={setMonitorIds}
              />
            )}
          </Field>
        </FieldGroup>
      </SlideoverBody>
      <SlideoverFooter>
        <Button type="submit" form="channel-form" disabled={save.isPending}>
          {channel ? "Save channel" : "Add channel"}
        </Button>
      </SlideoverFooter>
    </form>
  );
}

function SecretNotice({ secret, onDone }: { secret: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <Alert>
      <Webhook />
      <AlertTitle>Save the signing secret now</AlertTitle>
      <AlertDescription className="space-y-2">
        <p>You will not see it again. Use it to verify the X-Noria-Signature header.</p>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1 font-mono text-xs">
            {secret}
          </code>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void navigator.clipboard.writeText(secret);
              setCopied(true);
            }}
          >
            {copied ? <Check /> : <Copy />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button size="sm" variant="ghost" onClick={onDone}>
            Done
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}

function Deliveries({ channelId }: { channelId: string }) {
  const { formatDateTime } = useDateFormat();
  const query = useQuery({
    queryKey: queryKeys.channelDeliveries(channelId),
    queryFn: () => api<{ data: ChannelDelivery[] }>(endpoints.alertChannelDeliveries(channelId)),
  });
  const items = query.data?.data ?? [];
  if (query.isLoading) return <TableSkeleton rows={3} />;
  if (items.length === 0)
    return <p className="text-sm text-muted-foreground">Nothing has been sent yet.</p>;
  return (
    <ul className="divide-y divide-border">
      {items.map((item) => (
        <li key={item.id} className="py-3 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{eventLabels[item.event]}</Badge>
            <span className="font-medium">{item.monitor_name ?? "Test alert"}</span>
            <span
              className={cn(
                "ml-auto text-xs font-medium",
                item.status === "delivered"
                  ? "text-success"
                  : item.status === "failed"
                    ? "text-destructive"
                    : "text-muted-foreground",
              )}
            >
              {item.status === "pending" && item.attempts > 0
                ? `Retrying (${item.attempts})`
                : item.status}
            </span>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {formatDateTime(item.sent_at ?? item.created_at)}
          </p>
          {item.last_error && item.status !== "delivered" && (
            <p className="mt-1 font-mono text-xs break-all text-destructive">{item.last_error}</p>
          )}
        </li>
      ))}
    </ul>
  );
}

function ChannelsPanel() {
  const queryClient = useQueryClient();
  const canManage = useCanManage();
  const now = useNow();
  const [editing, setEditing, editingOpen, editingComplete] = useSlideoverState<
    AlertChannel | "new" | null
  >(null);
  const [history, setHistory, historyOpen, historyComplete] =
    useSlideoverState<AlertChannel | null>(null);
  const [removing, setRemoving, removingOpen, removingComplete] =
    useSlideoverState<AlertChannel | null>(null);
  const [secret, setSecret] = useState("");
  const query = useQuery({
    queryKey: queryKeys.alertChannels,
    queryFn: () => api<AlertChannelList>(endpoints.alertChannels()),
  });
  const channels = query.data?.data ?? [];

  const update = useMutation({
    mutationFn: ({ id, input }: { id: string; input: ChannelInput }) =>
      api<{ data: AlertChannel }>(endpoints.alertChannel(id), {
        method: "PATCH",
        body: JSON.stringify(input),
      }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: queryKeys.alertChannels }),
    onError: (err: Error) => errorToast(err, "We couldn't update the channel."),
  });
  const test = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.alertChannelTest(id), { method: "POST" }),
    onSuccess: (_, id) => {
      toast.success("Test alert queued. It should arrive within a few seconds.");
      window.setTimeout(() => {
        void queryClient.invalidateQueries({ queryKey: queryKeys.alertChannels });
        void queryClient.invalidateQueries({ queryKey: queryKeys.channelDeliveries(id) });
      }, 6000);
    },
    onError: (err: Error) => errorToast(err, "We couldn't send a test alert."),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.alertChannel(id), { method: "DELETE" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.alertChannels });
      setRemoving(null);
      toast.success("Channel removed.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't remove the channel."),
  });

  return (
    <div className="space-y-4">
      {secret && <SecretNotice secret={secret} onDone={() => setSecret("")} />}
      <Card className="py-0">
        <CardHeader className="flex flex-row items-center justify-between gap-3 p-6 pb-3">
          <CardTitle className="text-sm font-semibold">Channels</CardTitle>
          {canManage && channels.length > 0 && (
            <Button size="sm" onClick={() => setEditing("new")}>
              <Plus />
              Add channel
            </Button>
          )}
        </CardHeader>
        <CardContent className="p-0 pb-2">
          {query.isLoading ? (
            <TableSkeleton rows={3} />
          ) : channels.length === 0 ? (
            <EmptyState
              icon={BellRing}
              title="No alert channels yet"
              description="Monitors keep checking, but nobody hears about an outage until you add somewhere to send alerts."
              action={
                canManage ? (
                  <Button onClick={() => setEditing("new")}>
                    <Plus />
                    Add channel
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <ul className="divide-y divide-border">
              {channels.map((channel) => {
                const Icon = kindMeta[channel.kind].icon;
                return (
                  <li key={channel.id} className="flex flex-wrap items-center gap-3 px-6 py-4">
                    <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
                      <Icon className="size-4" />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-semibold">{channel.name}</span>
                        <Badge variant="outline">{kindMeta[channel.kind].label}</Badge>
                        <span className="text-xs text-muted-foreground">
                          {channel.all_monitors
                            ? "All monitors"
                            : `${channel.monitor_ids.length} monitors`}
                        </span>
                      </div>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {channelDestination(channel)}
                      </p>
                      {channel.last_error ? (
                        <p className="mt-1 text-xs break-all text-destructive">
                          Last delivery failed: {channel.last_error}
                        </p>
                      ) : (
                        <p className="mt-1 text-xs text-muted-foreground">
                          Last delivered{" "}
                          {formatRelative(channel.last_delivered_at, now).toLowerCase()}
                        </p>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <Switch
                        aria-label={`${channel.enabled ? "Disable" : "Enable"} ${channel.name}`}
                        checked={channel.enabled}
                        disabled={!canManage}
                        onCheckedChange={(enabled) =>
                          update.mutate({ id: channel.id, input: { enabled } })
                        }
                      />
                      <RowActions
                        label={`Actions for ${channel.name}`}
                        items={[
                          {
                            key: "history",
                            label: "Delivery history",
                            icon: CalendarClock,
                            onClick: () => setHistory(channel),
                          },
                          {
                            key: "test",
                            label: "Send test alert",
                            icon: Send,
                            disabled: test.isPending,
                            onClick: () => test.mutate(channel.id),
                          },
                          ...(canManage
                            ? [
                                {
                                  key: "edit",
                                  label: "Edit channel",
                                  icon: Pencil,
                                  onClick: () => setEditing(channel),
                                },
                                {
                                  key: "remove",
                                  label: "Remove channel",
                                  icon: Trash2,
                                  variant: "destructive" as const,
                                  separator: true,
                                  onClick: () => setRemoving(channel),
                                },
                              ]
                            : []),
                        ]}
                      />
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </CardContent>
      </Card>
      {!canManage && channels.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Only owners and admins can change alert channels.
        </p>
      )}

      <Slideover
        open={editingOpen}
        onOpenChange={(open) => !open && setEditing(null)}
        onOpenChangeComplete={editingComplete}
      >
        <SlideoverContent size="lg">
          <SlideoverHeader
            title={editing === "new" ? "Add alert channel" : "Edit alert channel"}
            description="Where we tell you when a monitor goes down, recovers, or its certificate is about to expire."
          />
          {editing !== null && (
            <ChannelForm
              key={editing === "new" ? "new" : editing.id}
              channel={editing === "new" ? undefined : editing}
              telegramAvailable={query.data?.telegram_available ?? false}
              telegramBot={query.data?.telegram_bot ?? ""}
              onSaved={(saved) => {
                setEditing(null);
                if (saved.signing_secret) setSecret(saved.signing_secret);
              }}
            />
          )}
        </SlideoverContent>
      </Slideover>

      <Slideover
        open={historyOpen}
        onOpenChange={(open) => !open && setHistory(null)}
        onOpenChangeComplete={historyComplete}
      >
        <SlideoverContent size="md">
          <SlideoverHeader
            title={history ? `Deliveries to ${history.name}` : "Deliveries"}
            description="The last 50 alerts sent through this channel."
          />
          <SlideoverBody>{history && <Deliveries channelId={history.id} />}</SlideoverBody>
        </SlideoverContent>
      </Slideover>

      <ActionDialog
        open={removingOpen}
        onOpenChangeComplete={removingComplete}
        onOpenChange={(open) => !open && setRemoving(null)}
        title={removing ? `Remove "${removing.name}"?` : "Remove channel?"}
        description="Alerts stop going to this destination immediately."
        icon={Trash2}
        tone="danger"
        confirmLabel="Remove channel"
        isLoading={remove.isPending}
        onConfirm={() => {
          if (removing) remove.mutate(removing.id);
        }}
      />
    </div>
  );
}

function toLocalInput(date: Date): string {
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function MaintenancePanel() {
  const queryClient = useQueryClient();
  const canManage = useCanManage();
  const { formatDateTime } = useDateFormat();
  const monitors = useMonitors().data?.data ?? [];
  const [title, setTitle] = useState("");
  const [starts, setStarts] = useState(() => toLocalInput(new Date()));
  const [ends, setEnds] = useState(() => toLocalInput(new Date(Date.now() + 3_600_000)));
  const [everything, setEverything] = useState(true);
  const [monitorIds, setMonitorIds] = useState<string[]>([]);
  const [error, setError] = useState("");
  const query = useQuery({
    queryKey: queryKeys.maintenance,
    queryFn: () => api<{ data: MaintenanceWindow[] }>(endpoints.maintenance()),
  });
  const windows = query.data?.data ?? [];

  const create = useMutation({
    mutationFn: (input: MaintenanceInput) =>
      api<{ data: { id: string } }>(endpoints.maintenance(), {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.maintenance });
      setTitle("");
      toast.success(
        "Maintenance window scheduled. Alerts for its monitors are silenced while it runs.",
      );
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status === 422) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't schedule the window.");
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.maintenance(id), { method: "DELETE" }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: queryKeys.maintenance }),
    onError: (err: Error) => errorToast(err, "We couldn't remove the window."),
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    if (!everything && monitorIds.length === 0) {
      setError("Pick the monitors under maintenance, or cover every monitor.");
      return;
    }
    create.mutate({
      title: title.trim(),
      starts_at: new Date(starts).toISOString(),
      ends_at: new Date(ends).toISOString(),
      monitor_ids: everything ? [] : monitorIds,
    });
  };

  const names = (ids: string[]) =>
    ids
      .map((id) => monitors.find((monitor) => monitor.id === id)?.name ?? "Deleted monitor")
      .join(", ");

  return (
    <div className="grid gap-4 lg:grid-cols-[1fr_22rem]">
      <Card className="py-0">
        <CardHeader className="p-6 pb-3">
          <CardTitle className="text-sm font-semibold">Windows</CardTitle>
        </CardHeader>
        <CardContent className="p-0 pb-2">
          {query.isLoading ? (
            <TableSkeleton rows={3} />
          ) : windows.length === 0 ? (
            <EmptyState
              icon={CalendarClock}
              title="No maintenance planned"
              description="Schedule a window before planned work. Monitors keep checking, incidents are still recorded, but nobody is paged."
            />
          ) : (
            <ul className="divide-y divide-border">
              {windows.map((window) => (
                <li key={window.id} className="flex flex-wrap items-start gap-3 px-6 py-4">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-semibold">{window.title}</span>
                      <Badge
                        variant="outline"
                        className={cn(
                          window.state === "active" && "border-info/30 bg-info/10 text-info",
                          window.state === "completed" && "text-muted-foreground",
                        )}
                      >
                        {window.state}
                      </Badge>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {formatDateTime(window.starts_at)} → {formatDateTime(window.ends_at)} ·{" "}
                      {window.all_monitors ? "All monitors" : names(window.monitor_ids)}
                    </p>
                  </div>
                  {canManage && window.state !== "completed" && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(window.id)}
                    >
                      {window.state === "active" ? "End now" : "Cancel"}
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {canManage ? (
        <Card>
          <CardHeader className="p-6 pb-3">
            <CardTitle className="text-sm font-semibold">Schedule maintenance</CardTitle>
          </CardHeader>
          <CardContent className="p-6 pt-2">
            <form onSubmit={submit} noValidate>
              <FieldGroup>
                {error && (
                  <Alert variant="destructive">
                    <AlertTriangle />
                    <AlertDescription>{error}</AlertDescription>
                  </Alert>
                )}
                <Field>
                  <FieldLabel htmlFor="maintenance-title" className="text-xs font-medium">
                    What is happening
                  </FieldLabel>
                  <Input
                    id="maintenance-title"
                    value={title}
                    maxLength={256}
                    placeholder="Database upgrade"
                    onChange={(event) => setTitle(event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="maintenance-start" className="text-xs font-medium">
                    Starts
                  </FieldLabel>
                  <Input
                    id="maintenance-start"
                    type="datetime-local"
                    value={starts}
                    onChange={(event) => setStarts(event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="maintenance-end" className="text-xs font-medium">
                    Ends
                  </FieldLabel>
                  <Input
                    id="maintenance-end"
                    type="datetime-local"
                    value={ends}
                    onChange={(event) => setEnds(event.target.value)}
                  />
                  <FieldDescription className="text-xs">
                    In your browser's time zone. Up to 7 days.
                  </FieldDescription>
                </Field>
                <Field>
                  <Field orientation="horizontal">
                    <Switch
                      id="maintenance-all"
                      checked={everything}
                      onCheckedChange={setEverything}
                    />
                    <FieldLabel htmlFor="maintenance-all" className="text-xs font-normal">
                      Every monitor
                    </FieldLabel>
                  </Field>
                  {!everything && (
                    <MonitorPicker
                      label="Monitors under maintenance"
                      selected={monitorIds}
                      onChange={setMonitorIds}
                    />
                  )}
                </Field>
                <Button type="submit" disabled={create.isPending || !title.trim()}>
                  Schedule
                </Button>
              </FieldGroup>
            </form>
          </CardContent>
        </Card>
      ) : (
        <p className="text-xs text-muted-foreground">
          Owners and admins schedule maintenance, because a window silences alerts.
        </p>
      )}
    </div>
  );
}

export function Alerts() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Alerts"
        description="Where outages are reported, and when to keep quiet during planned work."
        breadcrumbs={[{ label: "Alerts" }]}
      />
      <Tabs defaultValue="channels">
        <TabsList>
          <TabsTrigger value="channels">Channels</TabsTrigger>
          <TabsTrigger value="maintenance">Maintenance</TabsTrigger>
        </TabsList>
        <TabsContent value="channels" className="mt-4">
          <ChannelsPanel />
        </TabsContent>
        <TabsContent value="maintenance" className="mt-4">
          <MaintenancePanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
