import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ExternalLink,
  Globe,
  Plus,
  Radio,
  Trash2,
  X,
} from "lucide-react";
import type {
  StatusPage,
  StatusPageComponent,
  StatusPageInput,
  StatusPost,
  StatusPostImpact,
  StatusPostKind,
  StatusTheme,
} from "@/types";
import { APIError, api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import { MonitorStateDot, useMonitors } from "@/lib/monitors";
import { useDateFormat } from "@/context/timezone-context";
import { PageHeader } from "@/components/layout/Header";
import { ActionDialog } from "@/components/common/ActionDialog";
import { RowActions, copyRowValue } from "@/components/ui/row-actions";
import { EmptyState } from "@/components/common/EmptyState";
import { PageSkeleton, TableSkeleton } from "@/components/common/SkeletonLoaders";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";

const incidentStatuses = ["investigating", "identified", "monitoring", "resolved"];
const maintenanceStatuses = ["scheduled", "in_progress", "completed"];
const impacts: StatusPostImpact[] = ["none", "minor", "major", "critical"];

function statusesFor(kind: StatusPostKind) {
  return kind === "incident" ? incidentStatuses : maintenanceStatuses;
}

function label(value: string) {
  const text = value.replace("_", " ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function useStatusPages() {
  return useQuery({
    queryKey: queryKeys.statusPages,
    queryFn: () => api<{ data: StatusPage[] }>(endpoints.statusPages()),
  });
}

export function StatusPages() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const query = useStatusPages();
  const pages = query.data?.data ?? [];
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");

  const create = useMutation({
    mutationFn: (input: StatusPageInput) =>
      api<{ data: StatusPage }>(endpoints.statusPages(), {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.statusPages });
      void navigate(`/app/status-pages/${result.data.id}`);
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status < 500) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't create the status page.");
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    create.mutate({ title: title.trim() });
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Status pages"
        description="Public pages that tell your customers what is working, without them having to ask."
        breadcrumbs={[{ label: "Status pages" }]}
      />
      <Card>
        <CardContent className="p-6">
          <form onSubmit={submit} className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <Field className="flex-1">
              <FieldLabel htmlFor="status-title" className="text-xs font-medium">
                New status page
              </FieldLabel>
              <Input
                id="status-title"
                value={title}
                maxLength={128}
                placeholder="Acme status"
                onChange={(event) => setTitle(event.target.value)}
              />
            </Field>
            <Button type="submit" disabled={create.isPending || !title.trim()}>
              <Plus />
              Create page
            </Button>
          </form>
          {error && (
            <p className="mt-3 text-sm text-destructive">
              {error}{" "}
              {error.toLowerCase().includes("plan") && (
                <Link to="/app/billing">Upgrade your plan</Link>
              )}
            </p>
          )}
        </CardContent>
      </Card>

      <Card className="py-0">
        <CardContent className="p-0">
          {query.isLoading ? (
            <TableSkeleton rows={3} />
          ) : pages.length === 0 ? (
            <EmptyState
              icon={Radio}
              title="No status pages yet"
              description="Create one, choose which monitors it shows, and share the link with your customers."
            />
          ) : (
            <ul className="divide-y divide-border">
              {pages.map((page) => (
                <li key={page.id} className="flex flex-wrap items-center gap-3 px-6 py-4">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Link
                        to={`/app/status-pages/${page.id}`}
                        className="font-semibold hover:underline"
                      >
                        {page.title}
                      </Link>
                      {!page.published && <Badge variant="outline">Unpublished</Badge>}
                      {page.custom_domain && !page.custom_domain.verified && (
                        <Badge
                          variant="outline"
                          className="border-warning/30 bg-warning/10 text-warning"
                        >
                          Domain pending
                        </Badge>
                      )}
                    </div>
                    <p className="truncate font-mono text-xs text-muted-foreground">
                      {page.public_url}
                    </p>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {page.items} {page.items === 1 ? "component" : "components"}
                  </span>
                  <RowActions
                    label={`Actions for ${page.title}`}
                    items={[
                      {
                        key: "view",
                        label: "Manage status page",
                        icon: Radio,
                        to: `/app/status-pages/${page.id}`,
                      },
                      {
                        key: "public",
                        label: "View public page",
                        icon: ExternalLink,
                        href: page.public_url,
                      },
                      {
                        key: "copy",
                        label: "Copy public link",
                        icon: ExternalLink,
                        onClick: () => {
                          void copyRowValue(page.public_url, "Link");
                        },
                      },
                    ]}
                  />
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function SettingsCard({ page }: { page: StatusPage }) {
  const queryClient = useQueryClient();
  const [title, setTitle] = useState(page.title);
  const [slug, setSlug] = useState(page.slug);
  const [description, setDescription] = useState(page.description ?? "");
  const [homepage, setHomepage] = useState(page.homepage_url ?? "");
  const [logo, setLogo] = useState(page.logo_url ?? "");
  const [theme, setTheme] = useState<StatusTheme>(page.theme);
  const [responseTimes, setResponseTimes] = useState(page.show_response_times);
  const [published, setPublished] = useState(page.published);
  const [error, setError] = useState("");

  const save = useMutation({
    mutationFn: (input: StatusPageInput) =>
      api<{ data: StatusPage }>(endpoints.statusPage(page.id), {
        method: "PATCH",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.statusPages });
      toast.success("Status page saved. Visitors see the change within 30 seconds.");
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status < 500) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't save the status page.");
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setError("");
    save.mutate({
      title: title.trim(),
      slug: slug.trim(),
      description: description.trim(),
      homepage_url: homepage.trim(),
      logo_url: logo.trim(),
      theme,
      show_response_times: responseTimes,
      published,
    });
  };

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <CardTitle className="text-sm font-semibold">Page</CardTitle>
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
              <FieldLabel htmlFor="page-title" className="text-xs font-medium">
                Title
              </FieldLabel>
              <Input
                id="page-title"
                value={title}
                maxLength={128}
                onChange={(event) => setTitle(event.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="page-slug" className="text-xs font-medium">
                Address
              </FieldLabel>
              <Input
                id="page-slug"
                className="font-mono"
                value={slug}
                maxLength={48}
                onChange={(event) => setSlug(event.target.value.toLowerCase())}
              />
              <FieldDescription className="text-xs">
                Lowercase letters, digits and dashes. Changing it breaks old links.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="page-description" className="text-xs font-medium">
                Description
              </FieldLabel>
              <Textarea
                id="page-description"
                rows={2}
                maxLength={512}
                value={description}
                onChange={(event) => setDescription(event.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="page-homepage" className="text-xs font-medium">
                Your website
              </FieldLabel>
              <Input
                id="page-homepage"
                type="url"
                placeholder="https://example.com"
                value={homepage}
                onChange={(event) => setHomepage(event.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="page-logo" className="text-xs font-medium">
                Logo URL
              </FieldLabel>
              <Input
                id="page-logo"
                type="url"
                placeholder="https://example.com/logo.svg"
                value={logo}
                onChange={(event) => setLogo(event.target.value)}
              />
              <FieldDescription className="text-xs">
                An https image, shown about 36px tall. Leave empty to show the title.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel className="text-xs font-medium">Theme</FieldLabel>
              <Select
                value={theme}
                onValueChange={(value) => value && setTheme(value as StatusTheme)}
              >
                <SelectTrigger className="w-full sm:w-56" aria-label="Theme">
                  <SelectValue>
                    {(value: string) => (value === "auto" ? "Follow the visitor" : label(value))}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">Follow the visitor</SelectItem>
                  <SelectItem value="light">Light</SelectItem>
                  <SelectItem value="dark">Dark</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field orientation="horizontal">
              <Switch
                id="page-response"
                checked={responseTimes}
                onCheckedChange={setResponseTimes}
              />
              <FieldLabel htmlFor="page-response" className="text-xs font-normal">
                Show average response times
              </FieldLabel>
            </Field>
            <Field orientation="horizontal">
              <Switch id="page-published" checked={published} onCheckedChange={setPublished} />
              <FieldLabel htmlFor="page-published" className="text-xs font-normal">
                Published
              </FieldLabel>
            </Field>
            <Button type="submit" disabled={save.isPending}>
              Save page
            </Button>
          </FieldGroup>
        </form>
      </CardContent>
    </Card>
  );
}

interface Row {
  monitor_id: string;
  monitor_name: string;
  display_name: string;
  group_name: string;
  state: StatusPageComponent["state"];
}

function ComponentsCard({ page }: { page: StatusPage }) {
  const queryClient = useQueryClient();
  const monitors = useMonitors().data?.data ?? [];
  const [rows, setRows] = useState<Row[]>(() =>
    (page.components ?? []).map((item) => ({
      monitor_id: item.monitor_id,
      monitor_name: item.monitor_name,
      display_name: item.display_name ?? "",
      group_name: item.group_name ?? "",
      state: item.state,
    })),
  );
  const [adding, setAdding] = useState("");
  const available = monitors.filter(
    (monitor) => !rows.some((row) => row.monitor_id === monitor.id),
  );

  const save = useMutation({
    mutationFn: () =>
      api<{ data: StatusPage }>(endpoints.statusPageComponents(page.id), {
        method: "PUT",
        body: JSON.stringify({
          items: rows.map((row) => ({
            monitor_id: row.monitor_id,
            display_name: row.display_name,
            group_name: row.group_name,
          })),
        }),
      }),
    onSuccess: (result) => {
      queryClient.setQueryData(queryKeys.statusPage(page.id), result);
      void queryClient.invalidateQueries({ queryKey: queryKeys.statusPages, exact: true });
      toast.success("Components saved.");
    },
    onError: (err: Error) => errorToast(err, "We couldn't save the components."),
  });

  const move = (index: number, by: number) =>
    setRows((current) => {
      const next = [...current];
      const [row] = next.splice(index, 1);
      if (row) next.splice(index + by, 0, row);
      return next;
    });
  const update = (index: number, patch: Partial<Row>) =>
    setRows((current) => current.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <CardTitle className="text-sm font-semibold">Components</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 p-6 pt-2">
        <p className="text-xs leading-relaxed text-muted-foreground">
          Each component is one monitor, shown under a public name. Visitors never see its URL or
          host. Components with the same group are listed together, in this order.
        </p>
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No components yet.</p>
        ) : (
          <ul className="space-y-2">
            {rows.map((row, index) => (
              <li
                key={row.monitor_id}
                className="grid gap-2 rounded-xl border border-border p-3 sm:grid-cols-[auto_1fr_1fr_auto] sm:items-center"
              >
                <span className="flex items-center gap-2 text-sm font-medium">
                  <MonitorStateDot state={row.state} />
                  {row.monitor_name}
                </span>
                <Input
                  aria-label={`Public name for ${row.monitor_name}`}
                  placeholder={row.monitor_name}
                  value={row.display_name}
                  maxLength={128}
                  onChange={(event) => update(index, { display_name: event.target.value })}
                />
                <Input
                  aria-label={`Group for ${row.monitor_name}`}
                  placeholder="Group (optional)"
                  value={row.group_name}
                  maxLength={128}
                  onChange={(event) => update(index, { group_name: event.target.value })}
                />
                <span className="flex gap-1">
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label="Move up"
                    disabled={index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label="Move down"
                    disabled={index === rows.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Remove ${row.monitor_name}`}
                    onClick={() => setRows((current) => current.filter((_, i) => i !== index))}
                  >
                    <X />
                  </Button>
                </span>
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-col gap-2 sm:flex-row">
          <Select value={adding} onValueChange={(value) => setAdding(value ?? "")}>
            <SelectTrigger className="w-full sm:w-72" aria-label="Monitor to add">
              <SelectValue>
                {(value: string) =>
                  monitors.find((monitor) => monitor.id === value)?.name ?? "Choose a monitor"
                }
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {available.map((monitor) => (
                <SelectItem key={monitor.id} value={monitor.id}>
                  {monitor.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            variant="outline"
            disabled={!adding}
            onClick={() => {
              const monitor = monitors.find((item) => item.id === adding);
              if (!monitor) return;
              setRows((current) => [
                ...current,
                {
                  monitor_id: monitor.id,
                  monitor_name: monitor.name,
                  display_name: "",
                  group_name: "",
                  state: monitor.state,
                },
              ]);
              setAdding("");
            }}
          >
            <Plus />
            Add component
          </Button>
          <Button className="sm:ml-auto" disabled={save.isPending} onClick={() => save.mutate()}>
            Save components
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function DomainCard({ page }: { page: StatusPage }) {
  const queryClient = useQueryClient();
  const [hostname, setHostname] = useState(page.custom_domain?.hostname ?? "");
  const [error, setError] = useState("");
  const refresh = (result: { data: StatusPage }) => {
    queryClient.setQueryData(
      queryKeys.statusPage(page.id),
      (current: { data: StatusPage } | undefined) =>
        current ? { data: { ...current.data, ...result.data } } : result,
    );
    void queryClient.invalidateQueries({ queryKey: queryKeys.statusPages, exact: true });
  };
  const connect = useMutation({
    mutationFn: (value: string) =>
      api<{ data: StatusPage }>(endpoints.statusPage(page.id), {
        method: "PATCH",
        body: JSON.stringify({ custom_domain: value }),
      }),
    onSuccess: refresh,
    onError: (err: Error) => {
      if (err instanceof APIError && err.status < 500) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't connect the domain.");
    },
  });
  const verify = useMutation({
    mutationFn: () =>
      api<{ data: StatusPage }>(endpoints.statusPageVerify(page.id), { method: "POST" }),
    onSuccess: (result) => {
      refresh(result);
      toast.success("Domain verified. The certificate is issued on the first visit.");
    },
    onError: (err: Error) => {
      if (err instanceof APIError && err.status < 500) {
        setError(err.message);
        return;
      }
      errorToast(err, "We couldn't check the domain.");
    },
  });
  const domain = page.custom_domain;

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <CardTitle className="text-sm font-semibold">Custom domain</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 p-6 pt-2">
        {error && (
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertDescription>
              {error}{" "}
              {error.toLowerCase().includes("plan") && (
                <Link to="/app/billing">Upgrade your plan</Link>
              )}
            </AlertDescription>
          </Alert>
        )}
        <form
          className="flex flex-col gap-2 sm:flex-row"
          onSubmit={(event) => {
            event.preventDefault();
            setError("");
            connect.mutate(hostname.trim());
          }}
        >
          <Input
            aria-label="Custom domain"
            className="font-mono"
            placeholder="status.example.com"
            value={hostname}
            onChange={(event) => setHostname(event.target.value)}
          />
          <Button type="submit" variant="outline" disabled={connect.isPending}>
            <Globe />
            {hostname.trim() ? "Connect" : "Disconnect"}
          </Button>
        </form>
        {domain &&
          (domain.verified ? (
            <p className="text-sm text-success">
              {domain.hostname} is live. Visitors there see this page over HTTPS.
            </p>
          ) : (
            <div className="space-y-3 rounded-xl border border-border bg-muted/40 p-4 text-sm">
              <p>Add this record at your DNS provider, then check it:</p>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 font-mono text-xs">
                <dt className="text-muted-foreground">Type</dt>
                <dd>CNAME</dd>
                <dt className="text-muted-foreground">Name</dt>
                <dd className="break-all">{domain.hostname}</dd>
                <dt className="text-muted-foreground">Value</dt>
                <dd className="break-all">{domain.cname_target}</dd>
              </dl>
              <Button
                size="sm"
                disabled={verify.isPending}
                onClick={() => {
                  setError("");
                  verify.mutate();
                }}
              >
                Check DNS
              </Button>
            </div>
          ))}
      </CardContent>
    </Card>
  );
}

function NoticesCard({ page }: { page: StatusPage }) {
  const queryClient = useQueryClient();
  const { formatDateTime } = useDateFormat();
  const [kind, setKind] = useState<StatusPostKind>("incident");
  const [title, setTitle] = useState("");
  const [impact, setImpact] = useState<StatusPostImpact>("minor");
  const [body, setBody] = useState("");
  const [starts, setStarts] = useState("");
  const [ends, setEnds] = useState("");
  const [drafts, setDrafts] = useState<Record<string, { status: string; body: string }>>({});
  const [error, setError] = useState("");
  const query = useQuery({
    queryKey: queryKeys.statusPosts(page.id),
    queryFn: () => api<{ data: StatusPost[] }>(endpoints.statusPosts(page.id)),
  });
  const posts = query.data?.data ?? [];
  const invalidate = () =>
    void queryClient.invalidateQueries({ queryKey: queryKeys.statusPosts(page.id) });
  const fail = (err: Error) => {
    if (err instanceof APIError && err.status < 500) {
      setError(err.message);
      return;
    }
    errorToast(err, "We couldn't save the notice.");
  };

  const create = useMutation({
    mutationFn: () =>
      api<{ data: { id: string } }>(endpoints.statusPosts(page.id), {
        method: "POST",
        body: JSON.stringify({
          kind,
          title: title.trim(),
          impact: kind === "incident" ? impact : "none",
          body: body.trim(),
          starts_at: kind === "maintenance" && starts ? new Date(starts).toISOString() : undefined,
          ends_at: kind === "maintenance" && ends ? new Date(ends).toISOString() : undefined,
        }),
      }),
    onSuccess: () => {
      invalidate();
      setTitle("");
      setBody("");
      toast.success("Notice published.");
    },
    onError: fail,
  });
  const addUpdate = useMutation({
    mutationFn: ({ post, status, text }: { post: string; status: string; text: string }) =>
      api<unknown>(endpoints.statusPostUpdates(page.id, post), {
        method: "POST",
        body: JSON.stringify({ status, body: text }),
      }),
    onSuccess: (_, variables) => {
      invalidate();
      setDrafts((current) => ({ ...current, [variables.post]: { status: "", body: "" } }));
      toast.success("Update published.");
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: (post: string) =>
      api<void>(endpoints.statusPost(page.id, post), { method: "DELETE" }),
    onSuccess: invalidate,
    onError: fail,
  });

  return (
    <Card>
      <CardHeader className="p-6 pb-3">
        <CardTitle className="text-sm font-semibold">Notices</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6 p-6 pt-2">
        <p className="text-xs leading-relaxed text-muted-foreground">
          Outages detected by your monitors already show on the page. Notices are what you tell
          customers in your own words: what happened, what you are doing, and when it is fixed.
        </p>
        {error && (
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <form
          className="space-y-3 rounded-xl border border-border p-4"
          onSubmit={(event) => {
            event.preventDefault();
            setError("");
            create.mutate();
          }}
        >
          <div className="grid gap-3 sm:grid-cols-[10rem_1fr]">
            <Select
              value={kind}
              onValueChange={(value) => value && setKind(value as StatusPostKind)}
            >
              <SelectTrigger aria-label="Notice type">
                <SelectValue>{(value: string) => label(value)}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="incident">Incident</SelectItem>
                <SelectItem value="maintenance">Maintenance</SelectItem>
              </SelectContent>
            </Select>
            <Input
              aria-label="Notice title"
              placeholder={kind === "incident" ? "Elevated API errors" : "Database upgrade"}
              value={title}
              maxLength={256}
              onChange={(event) => setTitle(event.target.value)}
            />
          </div>
          {kind === "incident" ? (
            <Select
              value={impact}
              onValueChange={(value) => value && setImpact(value as StatusPostImpact)}
            >
              <SelectTrigger className="sm:w-56" aria-label="Impact">
                <SelectValue>{(value: string) => `${label(value)} impact`}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                {impacts.map((item) => (
                  <SelectItem key={item} value={item}>
                    {label(item)} impact
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <div className="grid gap-3 sm:grid-cols-2">
              <Input
                aria-label="Maintenance starts"
                type="datetime-local"
                value={starts}
                onChange={(event) => setStarts(event.target.value)}
              />
              <Input
                aria-label="Maintenance ends"
                type="datetime-local"
                value={ends}
                onChange={(event) => setEnds(event.target.value)}
              />
            </div>
          )}
          <Textarea
            aria-label="First update"
            rows={3}
            placeholder="What is happening and what you are doing about it."
            value={body}
            onChange={(event) => setBody(event.target.value)}
          />
          <Button type="submit" disabled={create.isPending || !title.trim() || !body.trim()}>
            Publish notice
          </Button>
        </form>

        {query.isLoading ? (
          <TableSkeleton rows={2} />
        ) : posts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No notices yet.</p>
        ) : (
          <ul className="space-y-4">
            {posts.map((post) => {
              const draft = drafts[post.id] ?? { status: "", body: "" };
              const open = post.resolved_at === null;
              const nextStatus =
                draft.status ||
                statusesFor(post.kind)[
                  Math.min(
                    statusesFor(post.kind).indexOf(post.status) + 1,
                    statusesFor(post.kind).length - 1,
                  )
                ] ||
                "";
              return (
                <li key={post.id} className="rounded-xl border border-border p-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold">{post.title}</span>
                    <Badge variant="outline">{label(post.kind)}</Badge>
                    <Badge
                      variant="outline"
                      className={open ? "border-warning/30 bg-warning/10 text-warning" : undefined}
                    >
                      {label(post.status)}
                    </Badge>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      className="ml-auto"
                      aria-label={`Delete ${post.title}`}
                      onClick={() => remove.mutate(post.id)}
                    >
                      <Trash2 />
                    </Button>
                  </div>
                  <ol className="mt-3 space-y-2 border-l border-border pl-4">
                    {post.updates.map((update) => (
                      <li key={update.id} className="text-sm">
                        <span className="font-medium">{label(update.status)}</span>
                        <span className="text-xs text-muted-foreground">
                          {" "}
                          · {formatDateTime(update.created_at)}
                        </span>
                        <p className="whitespace-pre-line text-muted-foreground">{update.body}</p>
                      </li>
                    ))}
                  </ol>
                  {open && (
                    <form
                      className="mt-3 grid gap-2 sm:grid-cols-[10rem_1fr_auto]"
                      onSubmit={(event) => {
                        event.preventDefault();
                        setError("");
                        addUpdate.mutate({
                          post: post.id,
                          status: nextStatus,
                          text: draft.body.trim(),
                        });
                      }}
                    >
                      <Select
                        value={nextStatus}
                        onValueChange={(value) =>
                          value &&
                          setDrafts((current) => ({
                            ...current,
                            [post.id]: { ...draft, status: value },
                          }))
                        }
                      >
                        <SelectTrigger aria-label="Update status">
                          <SelectValue>{(value: string) => label(value)}</SelectValue>
                        </SelectTrigger>
                        <SelectContent>
                          {statusesFor(post.kind).map((status) => (
                            <SelectItem key={status} value={status}>
                              {label(status)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <Input
                        aria-label="Update text"
                        placeholder="Post an update"
                        value={draft.body}
                        onChange={(event) =>
                          setDrafts((current) => ({
                            ...current,
                            [post.id]: { ...draft, body: event.target.value },
                          }))
                        }
                      />
                      <Button
                        type="submit"
                        variant="outline"
                        disabled={addUpdate.isPending || !draft.body.trim()}
                      >
                        Post
                      </Button>
                    </form>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

export function StatusPageDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const query = useQuery({
    queryKey: queryKeys.statusPage(id),
    queryFn: () => api<{ data: StatusPage }>(endpoints.statusPage(id)),
  });
  const remove = useMutation({
    mutationFn: () => api<void>(endpoints.statusPage(id), { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Status page deleted.");
      setConfirmDelete(false);
    },
    onError: (err: Error) => errorToast(err, "We couldn't delete the status page."),
  });

  if (query.isLoading) return <PageSkeleton />;
  const page = query.data?.data;
  if (!page) {
    return (
      <Card>
        <EmptyState
          icon={Radio}
          title="Status page not found"
          description="It may have been deleted, or it belongs to another workspace."
        />
      </Card>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={page.title}
        breadcrumbs={[{ label: "Status pages", to: "/app/status-pages" }, { label: page.title }]}
        action={
          <>
            <Button
              variant="outline"
              size="lg"
              nativeButton={false}
              render={
                <a href={page.public_url} target="_blank" rel="noreferrer" aria-label="View page" />
              }
            >
              <ExternalLink />
              View page
            </Button>
            <Button variant="destructive" size="lg" onClick={() => setConfirmDelete(true)}>
              <Trash2 />
              Delete
            </Button>
          </>
        }
      />
      {!page.published && (
        <Alert>
          <AlertTriangle />
          <AlertTitle>This page is not published</AlertTitle>
          <AlertDescription>Visitors get a not-found page until you publish it.</AlertDescription>
        </Alert>
      )}
      <div className="grid gap-6 lg:grid-cols-[1fr_24rem]">
        <div className="space-y-6">
          <ComponentsCard key={`components-${page.id}`} page={page} />
          <NoticesCard page={page} />
        </div>
        <div className="space-y-6">
          <SettingsCard key={`settings-${page.id}`} page={page} />
          <DomainCard key={`domain-${page.id}`} page={page} />
        </div>
      </div>
      <ActionDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        onOpenChangeComplete={(open) => {
          if (!open && remove.isSuccess) {
            void queryClient.invalidateQueries({ queryKey: queryKeys.statusPages });
            void navigate("/app/status-pages");
          }
        }}
        title={`Delete "${page.title}"?`}
        description="The public page and its custom domain stop working immediately. Your monitors are not affected."
        icon={Trash2}
        tone="danger"
        confirmLabel="Delete status page"
        isLoading={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </div>
  );
}
