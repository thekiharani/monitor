import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams, type Location } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Activity,
  AlertTriangle,
  ChevronDown,
  Globe,
  HeartPulse,
  Network,
  Plus,
  SearchCode,
  Trash2,
} from "lucide-react";
import type { Monitor, MonitorInput, MonitorKind, ProbeRegions, PublicPlan } from "@/types";
import { APIError, api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import { INTERVAL_OPTIONS, formatInterval, useAccountPlan } from "@/lib/monitors";
import { cn } from "@/lib/utils";
import { Monitors } from "@/pages/Monitors";
import { MonitorDetail } from "@/pages/MonitorDetail";
import {
  Slideover,
  SlideoverContent,
  SlideoverHeader,
  SlideoverFooter,
} from "@/components/ui/slideover";
import { EmptyState } from "@/components/common/EmptyState";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

const HTTP_METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];

const kindOptions: { value: MonitorKind; label: string; icon: typeof Globe }[] = [
  { value: "http", label: "HTTP(S)", icon: Globe },
  { value: "keyword", label: "Keyword", icon: SearchCode },
  { value: "tcp", label: "TCP port", icon: Network },
  { value: "heartbeat", label: "Heartbeat", icon: HeartPulse },
];

const HEARTBEAT_INTERVALS = [60, 300, 900, 1800, 3600, 21600, 43200, 86400];
const GRACE_OPTIONS = [0, 60, 300, 900, 1800, 3600];

const targetPlaceholders: Record<MonitorKind, string> = {
  http: "https://example.com/health",
  keyword: "https://example.com",
  tcp: "db.example.com:5432",
  heartbeat: "",
};

const targetHints: Record<MonitorKind, string> = {
  http: "A full http:// or https:// URL. Any expected status code counts as up.",
  keyword: "A full URL. We fetch the page and look for your keyword in the response body.",
  tcp: "A host and port, such as db.example.com:5432. Up means the port accepts a connection.",
  heartbeat: "",
};

interface HeaderRow {
  id: number;
  key: string;
  value: string;
}

type FormError = { code: string; message: string } | null;

export function NewMonitor() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(true);
  const savedPath = useRef<string | null>(null);
  const [pending, setPending] = useState(false);
  const location = useLocation();
  const backgroundLocation = (location.state as { backgroundLocation?: Location } | null)
    ?.backgroundLocation;
  const close = () => {
    if (pending) return;
    setOpen(false);
  };
  const complete = (isOpen: boolean) => {
    if (isOpen) return;
    if (savedPath.current) {
      void navigate(savedPath.current);
      return;
    }
    if (backgroundLocation) void navigate(-1);
    else void navigate("/app/monitors");
  };
  return (
    <>
      {!backgroundLocation && <Monitors />}
      <Slideover
        open={open}
        onOpenChange={(next) => !next && close()}
        onOpenChangeComplete={complete}
      >
        <SlideoverContent size="xl">
          <SlideoverHeader
            title="Add monitor"
            description="Choose what to check. We’ll take it from here."
            icon={Activity}
            showCloseButton={!pending}
          />
          <MonitorForm
            panel
            onCancel={close}
            onPendingChange={setPending}
            onSaved={(id) => {
              savedPath.current = `/app/monitors/${id}`;
              setOpen(false);
            }}
          />
        </SlideoverContent>
      </Slideover>
    </>
  );
}

export function EditMonitor() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [open, setOpen] = useState(true);
  const savedPath = useRef<string | null>(null);
  const [pending, setPending] = useState(false);
  const location = useLocation();
  const backgroundLocation = (location.state as { backgroundLocation?: Location } | null)
    ?.backgroundLocation;
  const close = () => {
    if (pending) return;
    setOpen(false);
  };
  const complete = (isOpen: boolean) => {
    if (isOpen) return;
    if (savedPath.current) {
      void navigate(savedPath.current);
      return;
    }
    if (backgroundLocation) void navigate(-1);
    else void navigate(`/app/monitors/${id}`);
  };
  const monitorQuery = useQuery({
    queryKey: queryKeys.monitor(id),
    queryFn: () => api<{ data: Monitor }>(endpoints.monitor(id)),
  });

  if (monitorQuery.isLoading) return <PageSkeleton />;
  const monitor = monitorQuery.data?.data;
  if (!monitor) {
    return (
      <Card className="gap-0 py-0">
        <EmptyState
          icon={Activity}
          title="Monitor not found"
          description="It may have been deleted, or it belongs to another workspace."
          action={
            <Button variant="outline" nativeButton={false} render={<Link to="/app/monitors" />}>
              Back to monitors
            </Button>
          }
        />
      </Card>
    );
  }

  return (
    <>
      {!backgroundLocation && <MonitorDetail />}
      <Slideover
        open={open}
        onOpenChange={(next) => !next && close()}
        onOpenChangeComplete={complete}
      >
        <SlideoverContent size="xl">
          <SlideoverHeader
            title={`Edit ${monitor.name}`}
            description="Changes apply from the next scheduled check."
            icon={Activity}
            showCloseButton={!pending}
          />
          <MonitorForm
            key={monitor.id}
            monitor={monitor}
            panel
            onCancel={close}
            onPendingChange={setPending}
            onSaved={(savedId) => {
              savedPath.current = `/app/monitors/${savedId}`;
              setOpen(false);
            }}
          />
        </SlideoverContent>
      </Slideover>
    </>
  );
}

function defaultInterval(plan: PublicPlan | undefined) {
  return Math.max(60, plan?.min_interval_seconds ?? 60);
}

function parseStatuses(value: string): number[] | null {
  const parts = value
    .split(/[\s,]+/)
    .map((part) => part.trim())
    .filter(Boolean);
  const codes = parts.map(Number);
  if (codes.some((code) => !Number.isInteger(code) || code < 100 || code > 599)) {
    return null;
  }
  return codes;
}

function optionalNumber(value: string): number | undefined {
  return value.trim() === "" ? undefined : Number(value);
}

export function MonitorForm({
  monitor,
  panel = false,
  onCancel,
  onPendingChange,
  onSaved,
}: {
  monitor?: Monitor;
  panel?: boolean;
  onCancel?: () => void;
  onPendingChange?: (pending: boolean) => void;
  onSaved?: (id: string) => void;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const plan = useAccountPlan();
  const regionsQuery = useQuery({
    queryKey: queryKeys.regions,
    queryFn: () => api<{ data: ProbeRegions }>(endpoints.regions()),
    staleTime: 300_000,
  });

  const editing = Boolean(monitor);
  const [name, setName] = useState(monitor?.name ?? "");
  const [kind, setKind] = useState<MonitorKind>(monitor?.kind ?? "http");
  const [target, setTarget] = useState(monitor?.target ?? "");
  const [chosenInterval, setChosenInterval] = useState<number | null>(
    monitor?.interval_seconds ?? null,
  );
  const [chosenRegions, setChosenRegions] = useState<string[] | null>(monitor?.regions ?? null);
  const [method, setMethod] = useState(monitor?.method || "GET");
  const nextHeaderId = useRef(Object.keys(monitor?.headers ?? {}).length);
  const [headers, setHeaders] = useState<HeaderRow[]>(() =>
    Object.entries(monitor?.headers ?? {}).map(([key, value], id) => ({ id, key, value })),
  );
  const [body, setBody] = useState(monitor?.body ?? "");
  const [statuses, setStatuses] = useState(monitor?.expected_statuses.join(", ") ?? "");
  const [keyword, setKeyword] = useState(monitor?.keyword ?? "");
  const [keywordAbsent, setKeywordAbsent] = useState(monitor?.keyword_absent ?? false);
  const [followRedirects, setFollowRedirects] = useState(monitor?.follow_redirects ?? true);
  const [timeoutMs, setTimeoutMs] = useState(monitor ? String(monitor.timeout_ms) : "");
  const [certWarnDays, setCertWarnDays] = useState(monitor ? String(monitor.cert_warn_days) : "");
  const [graceSeconds, setGraceSeconds] = useState(monitor?.grace_seconds ?? 300);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [formError, setFormError] = useState<FormError>(null);
  const [statusesError, setStatusesError] = useState("");

  const minInterval = plan?.min_interval_seconds ?? 0;
  const maxRegions = plan?.regions ?? Number.POSITIVE_INFINITY;
  const isHeartbeat = kind === "heartbeat";
  const interval = chosenInterval ?? (isHeartbeat ? 3600 : defaultInterval(plan));
  const availableRegions = regionsQuery.data?.data.regions ?? [];
  const onlineRegions = regionsQuery.data?.data.online;
  const defaultRegion = regionsQuery.data?.data.default;
  const regions = chosenRegions ?? (defaultRegion ? [defaultRegion] : []);
  const isHTTP = kind !== "tcp";

  const save = useMutation({
    mutationFn: ({ kind: createKind, ...changes }: MonitorInput) =>
      monitor
        ? api<{ data: Monitor }>(endpoints.monitor(monitor.id), {
            method: "PATCH",
            body: JSON.stringify(changes),
          })
        : api<{ data: Monitor }>(endpoints.monitors(), {
            method: "POST",
            body: JSON.stringify({ kind: createKind, ...changes }),
          }),
    onSuccess: (result) => {
      queryClient.setQueryData(queryKeys.monitor(result.data.id), result);
      void queryClient.invalidateQueries({ queryKey: queryKeys.monitors });
      toast.success(editing ? "Monitor saved." : "Monitor created. The first check is on its way.");
      if (onSaved) onSaved(result.data.id);
      else void navigate(`/app/monitors/${result.data.id}`);
    },
    onError: (err: Error) => {
      if (
        err instanceof APIError &&
        (err.code === "invalid_monitor" || err.code === "plan_limit_exceeded")
      ) {
        setFormError({ code: err.code, message: err.message });
        return;
      }
      errorToast(err, "We couldn't save the monitor. Please try again.");
    },
  });

  useEffect(() => {
    onPendingChange?.(save.isPending);
  }, [onPendingChange, save.isPending]);

  const buildInput = (): MonitorInput | null => {
    if (isHeartbeat) {
      return { name: name.trim(), kind, interval_seconds: interval, grace_seconds: graceSeconds };
    }
    const input: MonitorInput = {
      name: name.trim(),
      kind,
      target: target.trim(),
      interval_seconds: interval,
      regions,
      timeout_ms: optionalNumber(timeoutMs),
    };
    if (isHTTP) {
      const expected = parseStatuses(statuses);
      if (expected === null) {
        setStatusesError("Use status codes between 100 and 599, separated by commas.");
        setAdvancedOpen(true);
        return null;
      }
      input.method = method;
      input.headers = Object.fromEntries(
        headers.filter((row) => row.key.trim()).map((row) => [row.key.trim(), row.value]),
      );
      input.body = body === "" ? null : body;
      input.follow_redirects = followRedirects;
      input.cert_warn_days = optionalNumber(certWarnDays);
      if (expected.length > 0 || editing) input.expected_statuses = expected;
    }
    if (kind === "keyword") {
      input.keyword = keyword;
      input.keyword_absent = keywordAbsent;
    }
    return input;
  };

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (save.isPending) return;
    setFormError(null);
    setStatusesError("");
    const input = buildInput();
    if (input) save.mutate(input);
  };

  const updateHeader = (id: number, patch: Partial<HeaderRow>) =>
    setHeaders((rows) => rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));

  const toggleRegion = (region: string, on: boolean) =>
    setChosenRegions(on ? [...regions, region] : regions.filter((item) => item !== region));

  return (
    <form
      onSubmit={handleSubmit}
      className={panel ? "flex min-h-0 flex-1 flex-col" : "space-y-6"}
      noValidate
    >
      <div
        data-slot={panel ? "monitor-form-sections" : undefined}
        className={
          panel
            ? "min-h-0 flex-1 space-y-7 overflow-y-auto overscroll-contain px-6 py-7 sm:px-8"
            : "space-y-6"
        }
      >
        {formError && (
          <Alert variant={formError.code === "plan_limit_exceeded" ? "warning" : "destructive"}>
            <AlertTriangle />
            <AlertTitle>
              {formError.code === "plan_limit_exceeded"
                ? "Your plan does not allow this"
                : "Please check the monitor settings"}
            </AlertTitle>
            <AlertDescription>
              {formError.message}{" "}
              {formError.code === "plan_limit_exceeded" && (
                <Link to="/app/billing" className="font-semibold underline underline-offset-4">
                  Upgrade your plan
                </Link>
              )}
            </AlertDescription>
          </Alert>
        )}

        <Card className="gap-0 py-0">
          <CardHeader className="p-6 pb-3">
            <CardTitle className="text-sm font-semibold">What to check</CardTitle>
          </CardHeader>
          <CardContent className="p-6 pt-2">
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="monitor-name" className="text-xs font-medium">
                  Name
                </FieldLabel>
                <Input
                  id="monitor-name"
                  size="xl"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="Marketing site"
                  maxLength={128}
                  required
                />
              </Field>

              <Field>
                <FieldLabel className="text-xs font-medium" id="monitor-kind-label">
                  Type
                </FieldLabel>
                <ToggleGroup
                  aria-labelledby="monitor-kind-label"
                  variant="outline"
                  className="flex-wrap"
                  value={[kind]}
                  disabled={editing}
                  onValueChange={(value) => {
                    if (value[0]) setKind(value[0] as MonitorKind);
                  }}
                >
                  {kindOptions.map((option) => (
                    <ToggleGroupItem
                      key={option.value}
                      value={option.value}
                      className="gap-1.5 px-3 text-xs aria-pressed:border-primary/40 aria-pressed:bg-brand-soft aria-pressed:text-primary"
                    >
                      <option.icon />
                      {option.label}
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
                {editing && (
                  <FieldDescription className="text-xs">
                    The type is fixed once a monitor exists. Create a new monitor to change it.
                  </FieldDescription>
                )}
              </Field>

              {isHeartbeat ? (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  For cron jobs, backups and workers. Your job calls a private ping URL each time it
                  finishes, and we alert you when a ping does not arrive on time. You get the URL
                  once the monitor is created.
                </p>
              ) : (
                <Field>
                  <FieldLabel htmlFor="monitor-target" className="text-xs font-medium">
                    {kind === "tcp" ? "Host and port" : "URL"}
                  </FieldLabel>
                  <Input
                    id="monitor-target"
                    size="xl"
                    className="font-mono text-sm"
                    value={target}
                    onChange={(event) => setTarget(event.target.value)}
                    placeholder={targetPlaceholders[kind]}
                    inputMode={kind === "tcp" ? "text" : "url"}
                    autoComplete="off"
                    spellCheck={false}
                    required
                  />
                  <FieldDescription className="text-xs">{targetHints[kind]}</FieldDescription>
                </Field>
              )}

              {kind === "keyword" && (
                <Field>
                  <FieldLabel htmlFor="monitor-keyword" className="text-xs font-medium">
                    Keyword
                  </FieldLabel>
                  <Input
                    id="monitor-keyword"
                    size="xl"
                    value={keyword}
                    onChange={(event) => setKeyword(event.target.value)}
                    placeholder="All systems operational"
                    required
                  />
                  <Field orientation="horizontal" className="mt-1">
                    <Switch
                      id="monitor-keyword-absent"
                      checked={keywordAbsent}
                      onCheckedChange={setKeywordAbsent}
                    />
                    <FieldLabel htmlFor="monitor-keyword-absent" className="text-xs font-normal">
                      Alert when the keyword is present
                    </FieldLabel>
                  </Field>
                </Field>
              )}
            </FieldGroup>
          </CardContent>
        </Card>

        {isHeartbeat && (
          <Card className="gap-0 py-0">
            <CardHeader className="p-6 pb-3">
              <CardTitle className="text-sm font-semibold">Schedule</CardTitle>
            </CardHeader>
            <CardContent className="p-6 pt-2">
              <FieldGroup>
                <Field>
                  <FieldLabel className="text-xs font-medium">Expect a ping every</FieldLabel>
                  <Select
                    value={String(interval)}
                    onValueChange={(value) => value && setChosenInterval(Number(value))}
                  >
                    <SelectTrigger
                      size="xl"
                      className="w-full sm:w-56"
                      aria-label="Expected ping interval"
                    >
                      <SelectValue>{(value: string) => formatInterval(Number(value))}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {HEARTBEAT_INTERVALS.map((seconds) => (
                        <SelectItem key={seconds} value={String(seconds)}>
                          {formatInterval(seconds)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FieldDescription className="text-xs">
                    Match how often your job runs.
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel className="text-xs font-medium">Grace period</FieldLabel>
                  <Select
                    value={String(graceSeconds)}
                    onValueChange={(value) => value !== null && setGraceSeconds(Number(value))}
                  >
                    <SelectTrigger size="xl" className="w-full sm:w-56" aria-label="Grace period">
                      <SelectValue>
                        {(value: string) =>
                          Number(value) === 0 ? "None" : formatInterval(Number(value))
                        }
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {GRACE_OPTIONS.map((seconds) => (
                        <SelectItem key={seconds} value={String(seconds)}>
                          {seconds === 0 ? "None" : formatInterval(seconds)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FieldDescription className="text-xs">
                    Extra time a run may take before we call it missed.
                  </FieldDescription>
                </Field>
              </FieldGroup>
            </CardContent>
          </Card>
        )}

        {!isHeartbeat && (
          <Card className="gap-0 py-0">
            <CardHeader className="p-6 pb-3">
              <CardTitle className="text-sm font-semibold">How often and from where</CardTitle>
            </CardHeader>
            <CardContent className="p-6 pt-2">
              <FieldGroup>
                <Field>
                  <FieldLabel className="text-xs font-medium">Check interval</FieldLabel>
                  <Select
                    value={String(interval)}
                    onValueChange={(value) => value && setChosenInterval(Number(value))}
                  >
                    <SelectTrigger size="xl" className="w-full sm:w-56" aria-label="Check interval">
                      <SelectValue>
                        {(value: string) => `Every ${formatInterval(Number(value))}`}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {INTERVAL_OPTIONS.map((seconds) => {
                        const locked = seconds < minInterval;
                        return (
                          <SelectItem key={seconds} value={String(seconds)} disabled={locked}>
                            Every {formatInterval(seconds)}
                            {locked && (
                              <span className="ml-auto text-2xs font-semibold text-primary">
                                Upgrade
                              </span>
                            )}
                          </SelectItem>
                        );
                      })}
                    </SelectContent>
                  </Select>
                  {minInterval > (INTERVAL_OPTIONS[0] ?? 0) && (
                    <FieldDescription className="text-xs">
                      Your plan checks at most every {formatInterval(minInterval)}.{" "}
                      <Link to="/app/billing">Upgrade</Link> for faster checks.
                    </FieldDescription>
                  )}
                </Field>

                <Field>
                  <FieldLabel className="text-xs font-medium" id="monitor-regions-label">
                    Regions
                  </FieldLabel>
                  {regionsQuery.isLoading ? (
                    <p className="text-xs text-muted-foreground">Loading regions…</p>
                  ) : (
                    <div
                      role="group"
                      aria-labelledby="monitor-regions-label"
                      className="flex flex-wrap gap-2"
                    >
                      {availableRegions.map((region) => {
                        const selected = regions.includes(region);
                        const atLimit = !selected && regions.length >= maxRegions;
                        const offline =
                          onlineRegions !== undefined && !onlineRegions.includes(region);
                        return (
                          <button
                            key={region}
                            type="button"
                            title={
                              offline
                                ? "No probe is online in this region right now. Checks resume when it reconnects."
                                : undefined
                            }
                            aria-pressed={selected}
                            disabled={atLimit}
                            onClick={() => toggleRegion(region, !selected)}
                            className={cn(
                              "inline-flex h-8 items-center gap-1.5 rounded-lg border px-3 font-mono text-xs transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
                              selected
                                ? "border-primary/40 bg-brand-soft text-primary"
                                : "border-input hover:bg-muted",
                            )}
                          >
                            <Globe className="size-3.5" />
                            {region}
                            {offline && (
                              <span
                                className="size-1.5 rounded-full bg-status-warn"
                                aria-label="offline"
                              />
                            )}
                          </button>
                        );
                      })}
                    </div>
                  )}
                  <FieldDescription className="text-xs">
                    {Number.isFinite(maxRegions)
                      ? `Your plan checks from up to ${maxRegions} ${maxRegions === 1 ? "region" : "regions"}. `
                      : ""}
                    A monitor goes down when at least half of its regions confirm the failure.
                  </FieldDescription>
                  {regions.length === 0 && !regionsQuery.isLoading && (
                    <FieldError className="text-xs">Pick at least one region.</FieldError>
                  )}
                </Field>
              </FieldGroup>
            </CardContent>
          </Card>
        )}

        {!isHeartbeat && (
          <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
            <Card className="gap-0 py-0">
              <CollapsibleTrigger className="flex w-full items-center justify-between gap-3 p-6 text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50">
                <span>
                  <span className="block text-sm font-semibold">Advanced</span>
                  <span className="mt-0.5 block text-xs text-muted-foreground">
                    {isHTTP
                      ? "Method, headers, body, expected statuses, redirects, timeouts and certificates."
                      : "Connection timeout."}
                  </span>
                </span>
                <ChevronDown
                  className={cn(
                    "size-4 shrink-0 text-muted-foreground transition-transform",
                    advancedOpen && "rotate-180",
                  )}
                />
              </CollapsibleTrigger>
              <CollapsibleContent>
                <div className="border-t border-border p-6">
                  <FieldGroup>
                    {isHTTP && (
                      <>
                        <Field>
                          <FieldLabel className="text-xs font-medium">Method</FieldLabel>
                          <Select
                            value={method}
                            onValueChange={(value) => value && setMethod(value)}
                          >
                            <SelectTrigger size="xl" className="w-full sm:w-40" aria-label="Method">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {HTTP_METHODS.map((item) => (
                                <SelectItem key={item} value={item}>
                                  {item}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        </Field>

                        <Field>
                          <FieldLabel className="text-xs font-medium" id="monitor-headers-label">
                            Request headers
                          </FieldLabel>
                          <div
                            className="space-y-2"
                            role="group"
                            aria-labelledby="monitor-headers-label"
                          >
                            {headers.map((row) => (
                              <div key={row.id} className="flex gap-2">
                                <Input
                                  aria-label="Header name"
                                  value={row.key}
                                  onChange={(event) =>
                                    updateHeader(row.id, { key: event.target.value })
                                  }
                                  placeholder="Authorization"
                                  className="flex-1 font-mono text-xs"
                                />
                                <Input
                                  aria-label="Header value"
                                  value={row.value}
                                  onChange={(event) =>
                                    updateHeader(row.id, { value: event.target.value })
                                  }
                                  placeholder="Bearer …"
                                  className="flex-1 font-mono text-xs"
                                />
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="icon"
                                  aria-label="Remove header"
                                  onClick={() =>
                                    setHeaders((rows) => rows.filter((item) => item.id !== row.id))
                                  }
                                >
                                  <Trash2 />
                                </Button>
                              </div>
                            ))}
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              className="text-xs"
                              onClick={() => {
                                const id = nextHeaderId.current;
                                nextHeaderId.current += 1;
                                setHeaders((rows) => [...rows, { id, key: "", value: "" }]);
                              }}
                            >
                              <Plus />
                              Add header
                            </Button>
                          </div>
                          <FieldDescription className="text-xs">
                            Headers and the body are stored encrypted. Values that look like
                            credentials show as ******** — leave them unchanged to keep them.
                          </FieldDescription>
                        </Field>

                        <Field>
                          <FieldLabel htmlFor="monitor-body" className="text-xs font-medium">
                            Request body
                          </FieldLabel>
                          <Textarea
                            id="monitor-body"
                            value={body}
                            onChange={(event) => setBody(event.target.value)}
                            placeholder='{"ping": true}'
                            className="font-mono text-xs"
                            rows={3}
                          />
                        </Field>

                        <Field data-invalid={Boolean(statusesError)}>
                          <FieldLabel htmlFor="monitor-statuses" className="text-xs font-medium">
                            Expected status codes
                          </FieldLabel>
                          <Input
                            id="monitor-statuses"
                            value={statuses}
                            onChange={(event) => setStatuses(event.target.value)}
                            placeholder="200, 201, 204"
                            aria-invalid={Boolean(statusesError)}
                            className="font-mono text-xs"
                          />
                          {statusesError ? (
                            <FieldError className="text-xs">{statusesError}</FieldError>
                          ) : (
                            <FieldDescription className="text-xs">
                              Comma separated. Leave empty to accept any 2xx or 3xx response.
                            </FieldDescription>
                          )}
                        </Field>

                        <Field orientation="horizontal">
                          <Switch
                            id="monitor-follow-redirects"
                            checked={followRedirects}
                            onCheckedChange={setFollowRedirects}
                          />
                          <FieldContent>
                            <FieldLabel
                              htmlFor="monitor-follow-redirects"
                              className="text-xs font-medium"
                            >
                              Follow redirects
                            </FieldLabel>
                            <FieldDescription className="text-xs">
                              Judge the final response rather than the first redirect.
                            </FieldDescription>
                          </FieldContent>
                        </Field>
                      </>
                    )}

                    <div className="grid gap-5 sm:grid-cols-2">
                      <Field>
                        <FieldLabel htmlFor="monitor-timeout" className="text-xs font-medium">
                          Timeout (ms)
                        </FieldLabel>
                        <Input
                          id="monitor-timeout"
                          type="number"
                          inputMode="numeric"
                          min={1}
                          value={timeoutMs}
                          onChange={(event) => setTimeoutMs(event.target.value)}
                          placeholder="10000"
                        />
                      </Field>
                      {isHTTP && (
                        <Field>
                          <FieldLabel htmlFor="monitor-cert-warn" className="text-xs font-medium">
                            Certificate expiry warning (days)
                          </FieldLabel>
                          <Input
                            id="monitor-cert-warn"
                            type="number"
                            inputMode="numeric"
                            min={0}
                            value={certWarnDays}
                            onChange={(event) => setCertWarnDays(event.target.value)}
                            placeholder="14"
                          />
                        </Field>
                      )}
                    </div>
                  </FieldGroup>
                </div>
              </CollapsibleContent>
            </Card>
          </Collapsible>
        )}
      </div>
      <SlideoverFooter className={panel ? undefined : "border-0 bg-transparent px-0"}>
        <Button
          type="button"
          variant="ghost"
          size="xl"
          onClick={() =>
            onCancel
              ? onCancel()
              : navigate(monitor ? `/app/monitors/${monitor.id}` : "/app/monitors")
          }
          disabled={save.isPending}
        >
          Cancel
        </Button>
        <Button
          type="submit"
          size="xl"
          disabled={
            save.isPending ||
            !name.trim() ||
            (!isHeartbeat && (!target.trim() || regions.length === 0))
          }
        >
          {save.isPending ? "Saving…" : editing ? "Save changes" : "Create monitor"}
        </Button>
      </SlideoverFooter>
    </form>
  );
}
