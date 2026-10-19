import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUpRight,
  Check,
  Clock,
  Code2,
  Copy,
  KeyRound,
  Plus,
  ShieldAlert,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { errorToast } from "@/lib/toast";
import type { APIKey } from "@/types";
import { api, apiOrigin, endpoints, queryKeys } from "@/api";
import { PageHeader } from "@/components/layout/Header";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/common/EmptyState";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
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
import { ActionDialog } from "@/components/common/ActionDialog";
import { CodeBlock } from "@/components/common/CodeBlock";
import { useDateFormat } from "@/context/timezone-context";
import { cn } from "@/lib/utils";

export function APIKeys() {
  const { formatDate } = useDateFormat();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [revealedKey, setRevealedKey] = useState<string>("");
  const [copiedKey, setCopiedKey] = useState(false);
  const [keyToRevoke, setKeyToRevoke, revokeOpen, revokeComplete] =
    useSlideoverState<APIKey | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  const closeCreate = () => {
    if (createMutation.isPending) return;
    setCreateOpen(false);
  };

  const keysQuery = useQuery({
    queryKey: queryKeys.keys,
    queryFn: () => api<{ data: APIKey[] }>(endpoints.apiKeys()),
  });

  const createMutation = useMutation({
    mutationFn: () =>
      api<{ data: APIKey & { key: string } }>(endpoints.apiKeys(), {
        method: "POST",
        body: JSON.stringify({ name: name.trim() }),
      }),
    onSuccess: (data) => {
      setRevealedKey(data.data.key);
      setName("");
      void queryClient.invalidateQueries({ queryKey: queryKeys.keys });
      toast.success("API key generated successfully!");
    },
    onError: (err: Error) => {
      errorToast(err, "We couldn't create the API key. Please try again.");
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.apiKeys(id), { method: "DELETE" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.keys });
      toast.success("API key revoked");
      setKeyToRevoke(null);
    },
    onError: (err: Error) => {
      errorToast(err, "We couldn't revoke the API key. Please try again.");
    },
  });

  const handleCopy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopiedKey(true);
      toast.success("API key copied to clipboard!");
      setTimeout(() => setCopiedKey(false), 2000);
    } catch {
      errorToast(undefined, "We couldn't copy the API key.");
    }
  };

  const curlSample = `curl ${apiOrigin}/api/v1/me \\
  -H "Authorization: Bearer $NORIA_MONITOR_API_KEY"`;

  const keys = keysQuery.data?.data ?? [];
  const [renderedAt] = useState(() => Date.now());

  return (
    <div className="space-y-6">
      <PageHeader
        title="API keys"
        description="Keys that let your servers, pipelines and scripts call the Noria Monitor API."
        breadcrumbs={[{ label: "API Keys" }]}
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            Create API key
          </Button>
        }
      />

      <Slideover
        open={createOpen}
        onOpenChange={(open) => !open && closeCreate()}
        onOpenChangeComplete={(open) => {
          if (!open) {
            setName("");
            setRevealedKey("");
            setCopiedKey(false);
          }
        }}
      >
        <SlideoverContent size="md">
          <SlideoverHeader
            title={revealedKey ? "Your API key is ready" : "Create API key"}
            description="Use a separate key for each integration. Keep it server-side."
            icon={KeyRound}
            showCloseButton={!createMutation.isPending}
          />
          <form
            className="flex min-h-0 flex-1 flex-col"
            onSubmit={(e) => {
              e.preventDefault();
              if (!revealedKey && name.trim() && !createMutation.isPending) createMutation.mutate();
            }}
          >
            <SlideoverBody className="space-y-5">
              {!revealedKey && (
                <div className="space-y-2">
                  <Label htmlFor="api-key-name">Key name</Label>
                  <Input
                    id="api-key-name"
                    size="xl"
                    className="flex-1 text-xs"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="Key name, e.g. Next.js Production App"
                    required
                    maxLength={64}
                  />
                  <p className="text-sm text-muted-foreground">
                    A descriptive name helps you identify and revoke the right key later.
                  </p>
                </div>
              )}

              {revealedKey && (
                <div className="rounded-xl border border-warning/40 bg-warning/10 p-4 space-y-2">
                  <div className="flex items-center gap-2 text-xs font-medium text-warning">
                    <ShieldAlert className="size-4" />
                    <span>Copy this key now. It will not be shown again.</span>
                  </div>

                  <div className="flex items-center justify-between gap-2 rounded-xl bg-card p-3 border border-border">
                    <code className="truncate font-mono text-xs font-medium text-foreground">
                      {revealedKey}
                    </code>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => void handleCopy(revealedKey)}
                      className="h-7 shrink-0 rounded-lg text-xs"
                    >
                      {copiedKey ? (
                        <Check className="mr-1 size-3 text-success" />
                      ) : (
                        <Copy className="mr-1 size-3" />
                      )}
                      {copiedKey ? "Copied" : "Copy"}
                    </Button>
                  </div>
                </div>
              )}
            </SlideoverBody>
            <SlideoverFooter>
              <Button
                type="button"
                variant="outline"
                disabled={createMutation.isPending}
                onClick={closeCreate}
              >
                {revealedKey ? "Done" : "Cancel"}
              </Button>
              {!revealedKey && (
                <Button type="submit" disabled={createMutation.isPending || !name.trim()}>
                  <Plus />
                  {createMutation.isPending ? "Generating…" : "Create Key"}
                </Button>
              )}
            </SlideoverFooter>
          </form>
        </SlideoverContent>
      </Slideover>

      <Card className="gap-0 py-0">
        <CardContent className="p-6 space-y-3">
          <div className="flex items-center gap-2 text-primary">
            <ShieldCheck className="size-5" />
            <h3 className="font-semibold text-foreground text-sm">Security Best Practices</h3>
          </div>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Never expose your secret keys in frontend browser code or public GitHub repositories.
            Use server-side environment variables (
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-2xs">
              process.env.NORIA_MONITOR_API_KEY
            </code>
            ).
          </p>
        </CardContent>
      </Card>

      <Card className="gap-0 overflow-hidden py-0">
        <CardHeader className="flex flex-col items-start gap-3 border-b border-border p-4 pb-4 sm:flex-row sm:items-center sm:justify-between sm:p-6">
          <div>
            <div className="flex items-center gap-2">
              <Code2 className="size-4 text-primary" />
              <CardTitle className="text-base font-semibold">
                API Quickstart &amp; Snippets
              </CardTitle>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Ready-to-run programmatic integration with your active credentials.
            </p>
          </div>

          <a
            href="/docs"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-xl border border-border bg-card px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted"
          >
            Full API docs
            <ArrowUpRight className="size-3.5" />
          </a>
        </CardHeader>

        <div className="p-4 sm:p-6">
          <CodeBlock samples={{ curl: curlSample }} />
        </div>
      </Card>

      <Card className="gap-0 py-0">
        <CardHeader className="p-6 pb-3">
          <p className="text-xs font-medium text-muted-foreground">Active Credentials</p>
          <CardTitle className="mt-1 font-display text-lg font-semibold">Your API Keys</CardTitle>
        </CardHeader>

        <CardContent className="p-6 pt-2">
          {keysQuery.isLoading ? (
            <PageSkeleton />
          ) : keys.length > 0 ? (
            <div className="divide-y divide-border">
              {keys.map((key) => (
                <div
                  key={key.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3.5 first:pt-0 last:pb-0"
                >
                  <div className="flex min-w-0 items-center gap-3">
                    <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
                      <KeyRound className="size-4" />
                    </span>
                    <div className="min-w-0">
                      <p className="truncate text-xs font-medium text-foreground">{key.name}</p>
                      <p className="mt-0.5 truncate font-mono text-2xs text-muted-foreground">
                        {key.prefix}•••••••• · Created {formatDate(key.created_at)}
                      </p>
                      {key.status !== "active" && purgeDaysLeft(renderedAt, key) !== null && (
                        <p className="mt-1 flex items-center gap-1 text-2xs text-muted-foreground/80">
                          <Clock className="size-3" />
                          Will be deleted in {purgeDaysLeft(renderedAt, key)} days
                        </p>
                      )}
                    </div>
                  </div>

                  <div className="flex items-center gap-3">
                    <KeyStatusBadge status={key.status} />

                    <RowActions
                      label={`Actions for ${key.name}`}
                      items={[
                        {
                          key: "copy",
                          label: "Copy reference",
                          icon: Copy,
                          onClick: () => {
                            void copyRowValue(key.id);
                          },
                        },
                        ...(key.status === "active"
                          ? [
                              {
                                key: "revoke",
                                label: "Revoke key",
                                icon: Trash2,
                                variant: "destructive" as const,
                                separator: true,
                                onClick: () => setKeyToRevoke(key),
                              },
                            ]
                          : []),
                      ]}
                    />
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <EmptyState
              icon={KeyRound}
              title="No API keys generated"
              description="Create an API key to manage monitors and status pages through the REST API."
            />
          )}
        </CardContent>
      </Card>

      {keyToRevoke && (
        <ActionDialog
          open={revokeOpen}
          onOpenChangeComplete={revokeComplete}
          onOpenChange={(open) => !open && setKeyToRevoke(null)}
          title={`Revoke API key "${keyToRevoke.name}"?`}
          description="Any applications, integrations, or services using this key will immediately lose API access. This action cannot be undone."
          icon={KeyRound}
          tone="danger"
          targetDetails={[
            { label: "Key Name", value: keyToRevoke.name },
            { label: "Key Prefix", value: `${keyToRevoke.prefix}••••••••`, mono: true },
            { label: "Created", value: formatDate(keyToRevoke.created_at) },
          ]}
          confirmLabel="Revoke API Key"
          isLoading={revokeMutation.isPending}
          onConfirm={() => {
            revokeMutation.mutate(keyToRevoke.id);
          }}
        />
      )}
    </div>
  );
}

function KeyStatusBadge({ status }: { status: string }) {
  const tone =
    status === "active"
      ? "border-success/30 bg-success/10 text-success"
      : status === "expired"
        ? "border-warning/30 bg-warning/10 text-warning"
        : "border-destructive/30 bg-destructive/10 text-destructive";
  return (
    <Badge variant="outline" className={cn("capitalize", tone)}>
      {status === "active" || status === "expired" ? status : "revoked"}
    </Badge>
  );
}

function purgeDaysLeft(now: number, key: APIKey): number | null {
  if (key.status === "active") return null;
  const candidates = [key.expires_at, key.revoked_at]
    .filter((value): value is string => Boolean(value))
    .map((value) => new Date(value).getTime());
  if (candidates.length === 0) return null;
  const earliest = new Date(Math.min(...candidates));
  earliest.setUTCDate(earliest.getUTCDate() + 30);
  return Math.max(0, Math.ceil((earliest.getTime() - now) / 86_400_000));
}
