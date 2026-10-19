import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { Plans } from "@/pages/Control";
import type { ControlSession } from "@/control-api";

const plan = {
  plan: "starter",
  currency: "KES",
  price_minor: 50000,
  version: 0,
  kes_plan_code: "PLN_kes",
  usd_plan_code: "PLN_usd",
  monitors: 25,
  min_interval_seconds: 120,
  regions: 2,
  status_pages: 2,
  custom_domains: 1,
  alert_channels: 5,
  retention_days: 60,
  api_keys: 3,
  monthly_api_requests: 100000,
  team_members: 3,
  requests_per_second: 10,
  burst: 20,
};
const calls: Array<{ path: string; body: Record<string, unknown> }> = [];
const session: ControlSession = {
  principal_id: "operator",
  account_id: "account",
  email: "owner@example.test",
  capabilities: ["platform.billing.read", "platform.plans.manage"],
  known_capabilities: [],
};

vi.mock("@/control-api", async () => {
  const actual = await vi.importActual<typeof import("@/control-api")>("@/control-api");
  return {
    ...actual,
    controlFetch: vi.fn((path: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        calls.push({ path, body: JSON.parse(String(init.body)) });
        return Promise.resolve();
      }
      if (path === "/api/control/v1/plans") return Promise.resolve({ data: [plan] });
      if (path.startsWith("/api/control/v1/accounts"))
        return Promise.resolve({
          data: [
            { id: "account-1", email: "customer@example.test", plan: "free", status: "active" },
          ],
          pagination: { total: 1, offset: 0, limit: 10 },
        });
      throw new Error(`Unexpected request ${path}`);
    }),
  };
});

describe("Control plan management", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("edits Ksh prices and quotas with an audit reason and version", async () => {
    const user = userEvent.setup();
    renderApp(<Plans session={session} />, { route: "/control/plans" });
    expect(await screen.findByText("$5 / month")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Actions for starter plan" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit plan" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveStyle({ "--sheet-max-width": "56rem" });
    const price = within(dialog).getByLabelText("Monthly price (Ksh)");
    await user.clear(price);
    await user.type(price, "750");
    expect(within(dialog).getByText("$7.5")).toBeInTheDocument();
    const monitors = within(dialog).getByLabelText("Monitors");
    await user.clear(monitors);
    await user.type(monitors, "30");
    const save = within(dialog).getByRole("button", { name: "Save plan" });
    expect(save).toBeDisabled();
    await user.type(within(dialog).getByLabelText("Auditable Reason"), "Update launch offering");
    await user.click(save);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({
      path: "/api/control/v1/plans/starter",
      body: {
        currency: "KES",
        price_minor: 75000,
        monitors: 30,
        version: 0,
        reason: "Update launch offering",
        kes_plan_code: "PLN_kes",
        usd_plan_code: "PLN_usd",
      },
    });
  });

  it("shows the catalog without edits to a read-only operator", async () => {
    const user = userEvent.setup();
    renderApp(<Plans session={{ ...session, capabilities: ["platform.billing.read"] }} />, {
      route: "/control/plans",
    });
    expect(await screen.findByText("$5 / month")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit starter plan" })).not.toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Plan catalog" });
    expect(within(table).getByRole("columnheader", { name: "Monthly price" })).toBeInTheDocument();
    await user.click(within(table).getByRole("button", { name: "Actions for starter plan" }));
    await screen.findByRole("menu");
    expect(screen.queryByRole("menuitem", { name: "Edit plan" })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("menuitem", { name: "View plan" }));
    const panel = screen.getByRole("dialog", { name: "starter plan" });
    expect(panel).toHaveStyle({ "--sheet-max-width": "48rem" });
    const limits = within(panel).getByRole("table", { name: "Plan limits" });
    expect(within(limits).getByRole("row", { name: "Monitors 25" })).toBeInTheDocument();
    expect(
      within(limits).getByRole("row", { name: "Monthly API requests 100,000" }),
    ).toBeInTheDocument();
    expect(within(panel).getByText("PLN_kes")).toBeInTheDocument();
    expect(
      within(panel).queryByRole("button", { name: "Edit starter plan" }),
    ).not.toBeInTheDocument();
    expect(within(panel).queryByLabelText("Auditable Reason")).not.toBeInTheDocument();
  });

  it("expands the same detail panel into an editor without losing the selected plan", async () => {
    const user = userEvent.setup();
    renderApp(<Plans session={session} />, { route: "/control/plans" });
    await user.click(await screen.findByRole("button", { name: "Actions for starter plan" }));
    await user.click(await screen.findByRole("menuitem", { name: "View plan" }));
    const panel = screen.getByRole("dialog", { name: "starter plan" });
    expect(panel).toHaveStyle({ "--sheet-max-width": "48rem" });
    await user.click(within(panel).getByRole("button", { name: "Edit starter plan" }));
    expect(screen.getByRole("dialog", { name: "Edit starter plan" })).toBe(panel);
    expect(panel).toHaveStyle({ "--sheet-max-width": "56rem" });
    expect(within(panel).getByLabelText("Monthly price (Ksh)")).toHaveValue(500);
    expect(within(panel).getByLabelText("Monitors")).toHaveValue(25);
    expect(within(panel).getByRole("button", { name: "Save plan" })).toBeDisabled();
    await user.click(within(panel).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(calls).toHaveLength(0);
  });

  it("links directly to each account's plan controls", async () => {
    const user = userEvent.setup();
    renderApp(
      <Plans
        session={{
          ...session,
          capabilities: [
            ...session.capabilities,
            "platform.accounts.read",
            "platform.billing.comp",
          ],
        }}
      />,
      { route: "/control/plans" },
    );
    await user.click(
      await screen.findByRole("button", { name: "Actions for account customer@example.test" }),
    );
    expect(await screen.findByRole("menuitem", { name: "Manage plan" })).toHaveAttribute(
      "href",
      "/control/accounts/account-1#plan",
    );
  });
});
