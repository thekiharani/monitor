import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { Monitors } from "@/pages/Monitors";
import { NewMonitor } from "@/pages/MonitorForm";
import type { Monitor } from "@/types";
import type { ReactNode } from "react";
import { App } from "@/App";

vi.mock("@/components/layout/Shell", () => ({
  Shell: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

function monitor(overrides: Partial<Monitor>): Monitor {
  return {
    id: "m-1",
    name: "Marketing site",
    kind: "http",
    target: "https://example.com",
    method: "GET",
    headers: {},
    body: null,
    expected_statuses: [],
    keyword: null,
    keyword_absent: false,
    follow_redirects: true,
    interval_seconds: 300,
    timeout_ms: 10000,
    regions: ["eu-west"],
    cert_warn_days: 14,
    grace_seconds: 0,
    state: "up",
    state_changed_at: "2026-10-16T08:00:00Z",
    last_checked_at: "2026-10-16T09:00:00Z",
    last_status_code: 200,
    last_response_ms: 120,
    last_error: null,
    cert_expires_at: null,
    paused: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    uptime: { "1d": 100, "30d": 99.5 },
    ...overrides,
  };
}

const monitors = [
  monitor({ id: "m-1", name: "Marketing site", state: "up" }),
  monitor({ id: "m-2", name: "Billing API", target: "https://api.example.com", state: "down" }),
  monitor({
    id: "m-3",
    name: "Postgres",
    kind: "tcp",
    target: "db.example.com:5432",
    state: "paused",
  }),
];

const calls: { path: string; method: string; body: unknown }[] = [];

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, method, body });
      if (path === actual.endpoints.me) {
        return Promise.resolve({ data: { account: { plan: "free", role: "owner" } } });
      }
      if (path === actual.endpoints.plans()) {
        return Promise.resolve({
          data: [{ plan: "free", min_interval_seconds: 300, regions: 1, retention_days: 30 }],
        });
      }
      if (path === actual.endpoints.regions()) {
        return Promise.resolve({ data: { regions: ["eu-west", "us-east"], default: "eu-west" } });
      }
      if (path === actual.endpoints.monitors() && method === "POST") {
        return Promise.resolve({ data: monitor({ id: "m-new", ...(body as object) }) });
      }
      if (path === actual.endpoints.monitors()) {
        return Promise.resolve({ data: monitors });
      }
      return Promise.resolve({ data: [] });
    }),
  };
});

describe("Monitors list", () => {
  it("opens row actions without following the row link and copies only that row's reference", async () => {
    const user = userEvent.setup();
    const copy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    renderApp(<App />, { route: "/app/monitors" });
    const table = await screen.findByRole("table");
    await user.click(within(table).getByRole("button", { name: "Actions for Marketing site" }));
    expect(await screen.findByRole("menuitem", { name: "Edit monitor" })).toHaveAttribute(
      "href",
      "/app/monitors/m-1/edit",
    );
    await user.click(screen.getByRole("menuitem", { name: "Copy reference" }));
    await waitFor(() => expect(copy).toHaveBeenCalledWith("m-1"));
    expect(screen.getByRole("heading", { name: "Monitors" })).toBeInTheDocument();
  });

  it("preserves the list search when a monitor panel opens and closes", async () => {
    const user = userEvent.setup();
    renderApp(<App />, { route: "/app/monitors" });
    const search = await screen.findByLabelText("Search monitors");
    await user.type(search, "Billing");
    await user.click(screen.getByRole("button", { name: "Add monitor" }));
    const panel = await screen.findByRole("dialog", { name: "Add monitor" });
    await user.click(within(panel).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByLabelText("Search monitors")).toHaveValue("Billing");
    const table = screen.getByRole("table");
    expect(within(table).getByRole("link", { name: "Billing API" })).toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Marketing site" })).not.toBeInTheDocument();
  });

  it("narrows the list by state and by search", async () => {
    const user = userEvent.setup();
    renderApp(<Monitors />, { route: "/app/monitors" });

    const table = await screen.findByRole("table");
    for (const name of ["Marketing site", "Billing API", "Postgres"]) {
      expect(within(table).getByRole("link", { name })).toBeInTheDocument();
    }

    const filter = screen.getByRole("group", { name: "Filter by state" });
    await user.click(within(filter).getByRole("button", { name: /Down/ }));

    expect(within(table).getByRole("link", { name: "Billing API" })).toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Marketing site" })).not.toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Postgres" })).not.toBeInTheDocument();

    await user.click(within(filter).getByRole("button", { name: /All/ }));
    await user.type(screen.getByLabelText("Search monitors"), "5432");

    expect(within(table).getByRole("link", { name: "Postgres" })).toBeInTheDocument();
    expect(within(table).queryByRole("link", { name: "Billing API" })).not.toBeInTheDocument();
  });
});

describe("Monitor form", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("opens monitor creation as a right-hand panel with a separate scrolling body and footer", async () => {
    renderApp(<NewMonitor />, { route: "/app/monitors/new" });
    const panel = await screen.findByRole("dialog", { name: "Add monitor" });
    expect(panel).toHaveAttribute("data-side", "right");
    const footer = panel.querySelector('[data-slot="sheet-footer"]')!;
    expect(
      within(footer as HTMLElement).getByRole("button", { name: "Create monitor" }),
    ).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Close panel" })).toBeInTheDocument();
  });

  it("holds intervals and regions to the plan's limits", async () => {
    const user = userEvent.setup();
    renderApp(<NewMonitor />, { route: "/app/monitors/new" });

    const interval = await screen.findByRole("combobox", { name: "Check interval" });
    await waitFor(() => expect(interval).toHaveTextContent("Every 5m"));

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "eu-west" })).toHaveAttribute(
        "aria-pressed",
        "true",
      ),
    );
    expect(screen.getByRole("button", { name: "us-east" })).toBeDisabled();

    await user.click(interval);
    const fast = await screen.findByRole("option", { name: /Every 1m/ });
    expect(fast).toHaveAttribute("aria-disabled", "true");
    expect(fast).toHaveTextContent("Upgrade");
    expect(screen.getByRole("option", { name: /Every 5m/ })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await user.keyboard("{Escape}");

    await user.type(screen.getByLabelText("Name"), "Marketing site");
    await user.type(screen.getByLabelText("URL"), "https://example.com");
    await user.click(screen.getByRole("button", { name: "Create monitor" }));

    await waitFor(() => {
      const create = calls.find((call) => call.method === "POST");
      expect(create?.body).toMatchObject({
        name: "Marketing site",
        kind: "http",
        target: "https://example.com",
        interval_seconds: 300,
        regions: ["eu-west"],
      });
    });
  });
});

describe("Heartbeat monitors", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("drop the target, regions and plan interval floor", async () => {
    const user = userEvent.setup();
    renderApp(<NewMonitor />, { route: "/app/monitors/new" });

    await user.click(await screen.findByRole("button", { name: /Heartbeat/ }));
    expect(screen.queryByLabelText("URL")).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Regions" })).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Expected ping interval" })).toHaveTextContent(
      "1h",
    );

    await user.type(screen.getByLabelText("Name"), "Nightly backup");
    await user.click(screen.getByRole("button", { name: "Create monitor" }));

    await waitFor(() => {
      const create = calls.find((call) => call.method === "POST");
      expect(create?.body).toEqual({
        name: "Nightly backup",
        kind: "heartbeat",
        interval_seconds: 3600,
        grace_seconds: 300,
      });
    });
  });
});
