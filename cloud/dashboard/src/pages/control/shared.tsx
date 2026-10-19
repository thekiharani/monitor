import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Copy, Eye, RefreshCw, UserRound } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyTitle } from "@/components/ui/empty";
import { Label } from "@/components/ui/label";
import { RowActions, copyRowValue, type RowActionItem } from "@/components/ui/row-actions";
import {
  Slideover,
  SlideoverBody,
  SlideoverContent,
  SlideoverFooter,
  SlideoverHeader,
  useSlideoverState,
} from "@/components/ui/slideover";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

export { TableCell, TableHead, TableRow };
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  controlFetch,
  controlListPath,
  hasControlCapability,
  cellString,
  cellText,
  type ControlRow,
  type ControlSession,
} from "@/control-api";

export function formatControlDate(value: unknown) {
  if (!value) return "—";
  const date = new Date(String(value));
  return Number.isNaN(date.valueOf())
    ? String(value)
    : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

export function StatusPill({ value }: { value: string }) {
  const positive = ["active", "successful", "resolved", "sent", "pro", "business"].includes(value);
  const negative = [
    "suspended",
    "blocked",
    "failed",
    "new",
    "past_due",
    "revoked",
    "closed",
    "canceled",
  ].includes(value);
  return (
    <span
      className={cn(
        "inline-flex rounded-md px-1.5 py-0.5 text-2xs font-medium capitalize",
        positive && "bg-success/10 text-success",
        negative && "bg-warning/15 text-warning",
        !positive && !negative && "bg-muted text-muted-foreground",
      )}
    >
      {value.replaceAll("_", " ")}
    </span>
  );
}

export function ControlTable({ headers, children }: { headers: string[]; children: ReactNode }) {
  return (
    <div className="overflow-hidden rounded-xl bg-card ring-1 ring-border">
      <Table className="min-w-180 text-left text-xs">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            {headers.map((header) => (
              <TableHead
                key={header}
                className={cn(
                  "h-10 px-4 text-xs font-normal text-muted-foreground",
                  (header === "Actions" || header === "Action" || header === "") &&
                    "w-20 text-right",
                )}
              >
                {header}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>{children}</TableBody>
      </Table>
    </div>
  );
}

const emptyRowActions: RowActionItem[] = [];
const sensitiveControlField = /secret|password|token|hash|encrypted/i;

function controlDetailText(field: string, value: ControlRow[string]) {
  if (sensitiveControlField.test(field)) return "Redacted";
  if (value && typeof value === "object") {
    return JSON.stringify(
      value,
      (key, nested: unknown) => (sensitiveControlField.test(key) ? "Redacted" : nested),
      2,
    );
  }
  return cellText(value);
}

export function ControlRowActions({
  row,
  label,
  session,
  items = emptyRowActions,
}: {
  row: ControlRow;
  label?: string;
  session?: ControlSession;
  items?: RowActionItem[];
}) {
  const [selected, setSelected, open, complete] = useSlideoverState<ControlRow | null>(null);
  const reference =
    cellString(row.provider_reference) ||
    cellString(row.provider_subscription_code) ||
    cellString(row.id) ||
    cellString(row.account_id) ||
    cellString(row.principal_id);
  const name =
    label ||
    cellString(row.name) ||
    cellString(row.email) ||
    cellString(row.to_email) ||
    reference ||
    "record";
  const accountId = cellString(row.account_id);
  return (
    <>
      <RowActions
        label={`Actions for ${name}`}
        items={[
          { key: "view", label: "View", icon: Eye, onClick: () => setSelected(row) },
          ...(accountId && hasControlCapability(session, "platform.accounts.read")
            ? [
                {
                  key: "account",
                  label: "View account",
                  icon: UserRound,
                  to: `/control/accounts/${encodeURIComponent(accountId)}`,
                },
              ]
            : []),
          ...items.filter((item) => item.variant !== "destructive"),
          ...(reference
            ? [
                {
                  key: "copy",
                  label: "Copy reference",
                  icon: Copy,
                  onClick: () => {
                    void copyRowValue(reference);
                  },
                },
              ]
            : []),
          ...items
            .filter((item) => item.variant === "destructive")
            .map((item, index) =>
              Object.assign({}, item, { separator: index === 0 || item.separator }),
            ),
        ]}
      />
      <Slideover
        open={open}
        onOpenChange={(next) => !next && setSelected(null)}
        onOpenChangeComplete={complete}
      >
        <SlideoverContent size="lg">
          <SlideoverHeader title="Record details" description={name} icon={Eye} />
          <SlideoverBody>
            <dl className="divide-y rounded-lg border px-5 text-sm">
              {selected &&
                Object.entries(selected).map(([key, value]) => (
                  <div key={key} className="grid gap-2 py-4 sm:grid-cols-[10rem_minmax(0,1fr)]">
                    <dt className="capitalize text-muted-foreground">{key.replaceAll("_", " ")}</dt>
                    <dd className="min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">
                      {controlDetailText(key, value)}
                    </dd>
                  </div>
                ))}
            </dl>
          </SlideoverBody>
          <SlideoverFooter>
            <Button variant="outline" onClick={() => setSelected(null)}>
              Done
            </Button>
          </SlideoverFooter>
        </SlideoverContent>
      </Slideover>
    </>
  );
}

export function ControlEmpty({ label = "No records found" }: { label?: string }) {
  return (
    <Empty className="rounded-xl border border-dashed border-border p-10 text-sm text-muted-foreground">
      <EmptyTitle className="font-normal">{label}</EmptyTitle>
    </Empty>
  );
}

export function ConsoleHeader({
  title,
  description,
  onRefresh,
  isFetching,
}: {
  title: string;
  description: string;
  onRefresh?: () => void;
  isFetching?: boolean;
}) {
  return (
    <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <p className="mb-1 text-xs font-medium text-muted-foreground">Control plane</p>
        <h1 className="font-display text-2xl font-semibold sm:text-[1.75rem] sm:leading-9">
          {title}
        </h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{description}</p>
      </div>
      {onRefresh && (
        <Button variant="outline" className="gap-2" onClick={onRefresh}>
          <RefreshCw className={cn("size-4", isFetching && "animate-spin")} />
          Refresh
        </Button>
      )}
    </div>
  );
}

export function Metric({
  label,
  value,
  icon: Icon,
  tone = "primary",
  to,
}: {
  label: string;
  value: string | number;
  icon: typeof RefreshCw;
  tone?: "primary" | "warning" | "success";
  to?: string;
}) {
  const card = (
    <Card className={cn("gap-0 py-0", to && "transition-colors hover:bg-muted/40")}>
      <CardContent className="p-4 sm:p-5">
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Icon
            className={cn(
              "size-3.5",
              tone === "warning"
                ? "text-warning"
                : tone === "success"
                  ? "text-success"
                  : "text-primary",
            )}
          />
          {label}
        </p>
        <p className="tnum mt-3 text-2xl">
          {typeof value === "number" ? value.toLocaleString() : value}
        </p>
      </CardContent>
    </Card>
  );
  if (!to) return card;
  return (
    <Link
      to={to}
      className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded-xl"
    >
      {card}
    </Link>
  );
}

export function useFilterParam(name: string, fallback: string) {
  const [params, setParams] = useSearchParams();
  const value = params.get(name) ?? fallback;
  const setValue = useCallback(
    (next: string) => {
      setParams(
        (current) => {
          const updated = new URLSearchParams(current);
          if (next === "" || next === fallback) updated.delete(name);
          else updated.set(name, next);
          return updated;
        },
        { replace: true },
      );
    },
    [name, fallback, setParams],
  );
  return [value, setValue] as const;
}

export function useControlData(path: string, enabled = true) {
  return useQuery({
    queryKey: ["control", path],
    queryFn: () => controlFetch<{ data: ControlRow[] }>(path).then((body) => body.data),
    enabled,
    staleTime: 10_000,
  });
}

export const CONTROL_PAGE_SIZE = 10;
export const CONTROL_PAGE_SIZES = [5, 10, 25, 50, 100] as const;

function pageParamKeys(paramKey?: string) {
  return {
    page: paramKey ? `${paramKey}_page` : "page",
    limit: paramKey ? `${paramKey}_limit` : "limit",
  };
}

function parsePageParam(raw: string | null) {
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1) return 1;
  return value;
}

function parseLimitParam(raw: string | null) {
  const value = Number(raw);
  return (CONTROL_PAGE_SIZES as readonly number[]).includes(value) ? value : CONTROL_PAGE_SIZE;
}

export type ControlPagination = {
  offset: number;
  limit: number;
  total: number;
};

export type ControlListResponse = {
  data: ControlRow[];
  pagination: ControlPagination;
};

export function useControlPage(
  path: string,
  params: Record<string, string | number | undefined> = {},
  enabled = true,
  paramKey?: string,
) {
  const [searchParams, setSearchParams] = useSearchParams();
  const keys = pageParamKeys(paramKey);
  const pageSize = parseLimitParam(searchParams.get(keys.limit));
  const page = parsePageParam(searchParams.get(keys.page));
  const offset = (page - 1) * pageSize;

  const filterKey = JSON.stringify({ path, ...params });
  const previousFilterKey = useRef(filterKey);
  useEffect(() => {
    if (previousFilterKey.current === filterKey) return;
    previousFilterKey.current = filterKey;
    setSearchParams(
      (current) => {
        if (!current.has(keys.page)) return current;
        const next = new URLSearchParams(current);
        next.delete(keys.page);
        return next;
      },
      { replace: true },
    );
  }, [filterKey, keys.page, setSearchParams]);

  const setOffset = (nextOffset: number) => {
    const nextPage = Math.max(1, Math.floor(nextOffset / pageSize) + 1);
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (nextPage <= 1) next.delete(keys.page);
        else next.set(keys.page, String(nextPage));
        return next;
      },
      { replace: true },
    );
  };

  const setPageSize = (size: number) => {
    const nextSize = (CONTROL_PAGE_SIZES as readonly number[]).includes(size)
      ? size
      : CONTROL_PAGE_SIZE;
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.delete(keys.page);
        if (nextSize === CONTROL_PAGE_SIZE) next.delete(keys.limit);
        else next.set(keys.limit, String(nextSize));
        return next;
      },
      { replace: true },
    );
  };

  const query = useQuery({
    queryKey: ["control", "page", path, params, offset, pageSize],
    queryFn: () =>
      controlFetch<ControlListResponse>(
        controlListPath(path, {
          ...params,
          limit: pageSize,
          offset,
        }),
      ),
    enabled,
    staleTime: 10_000,
  });

  return {
    ...query,
    rows: query.data?.data ?? [],
    pagination: query.data?.pagination,
    offset,
    setOffset,
    pageSize,
    setPageSize,
  };
}

export function ControlPager({
  query,
}: {
  query: {
    offset: number;
    setOffset: (offset: number) => void;
    pageSize: number;
    setPageSize: (size: number) => void;
    pagination?: ControlPagination;
    isFetching: boolean;
  };
}) {
  const page = query.pagination;
  if (!page || page.total === 0) return null;
  const from = page.offset + 1;
  const to = Math.min(page.offset + page.limit, page.total);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 pt-2 text-xs text-muted-foreground">
      <span>
        {from}–{to} of {page.total}
      </span>
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center gap-2">
          <Label htmlFor="control-page-size">Per page</Label>
          <Select
            value={String(query.pageSize)}
            onValueChange={(value) => query.setPageSize(Number(value ?? CONTROL_PAGE_SIZES[0]))}
            disabled={query.isFetching}
          >
            <SelectTrigger size="sm" id="control-page-size" className="w-20">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CONTROL_PAGE_SIZES.map((size) => (
                <SelectItem key={size} value={String(size)}>
                  {size}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button
          variant="outline"
          className="rounded-lg"
          disabled={query.isFetching || page.offset === 0}
          onClick={() => query.setOffset(Math.max(0, page.offset - page.limit))}
        >
          Previous
        </Button>
        <Button
          variant="outline"
          className="rounded-lg"
          disabled={query.isFetching || page.offset + page.limit >= page.total}
          onClick={() => query.setOffset(page.offset + page.limit)}
        >
          Next
        </Button>
      </div>
    </div>
  );
}

export function useDebouncedValue(value: string, delay = 300) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

export function RequireCapability({
  session,
  capability,
  children,
}: {
  session: ControlSession;
  capability: string;
  children: ReactNode;
}) {
  if (!hasControlCapability(session, capability)) {
    return <ControlEmpty label="You do not have access to this section" />;
  }
  return children;
}

export function monoCell(value: unknown) {
  return String(value || "—");
}
