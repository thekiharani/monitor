import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { Billing } from "@/pages/Billing";
import type { PublicPlan } from "@/types";

const plans: PublicPlan[] = (["free", "starter", "pro", "business"] as const).map(
  (plan, index) => ({
    plan,
    currency: "KES",
    price_minor: [0, 80000, 200000, 500000][index]!,
    monitors: [10, 25, 50, 200][index]!,
    min_interval_seconds: [300, 120, 60, 30][index]!,
    regions: index + 1,
    status_pages: index + 1,
    custom_domains: index,
    alert_channels: index + 2,
    retention_days: 30 * (index + 1),
    api_keys: index + 1,
    monthly_api_requests: 10000,
    team_members: index + 1,
  }),
);
let subscription: {
  currency: "KES" | "USD";
  status: string;
  current_period_end: null;
  billing_mode: "auto";
} | null = null;
let plansFailed = false;
const calls: string[] = [];

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string, init?: RequestInit) => {
      if (path === actual.endpoints.billing)
        return Promise.resolve({ data: { plan: subscription ? "pro" : "free", subscription } });
      if (path === actual.endpoints.plans())
        return plansFailed
          ? Promise.reject(new Error("Plans unavailable"))
          : Promise.resolve({ data: plans });
      if (init?.method === "POST") {
        calls.push(path);
        return Promise.resolve({ data: { status: "active" } });
      }
      return Promise.reject(new Error(`Unexpected request ${path}`));
    }),
  };
});

describe("Billing currencies", () => {
  beforeEach(() => {
    subscription = null;
    plansFailed = false;
    calls.length = 0;
  });

  it("uses configured Ksh prices as the base and USD as KES divided by 100", async () => {
    renderApp(<Billing />, { route: "/app/billing" });
    expect(await screen.findByText(/^Ksh 800/)).toBeInTheDocument();
    expect(screen.getByText("$8 / month")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Kenyan shilling (Ksh)" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("charges the selected USD currency by card", async () => {
    const user = userEvent.setup();
    renderApp(<Billing />, { route: "/app/billing" });
    await user.click(await screen.findByRole("button", { name: "US dollar ($)" }));
    expect(screen.getByText(/^\$8$/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "M-Pesa" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upgrade to Starter" }));
    await waitFor(() =>
      expect(calls).toContain(
        "/api/dashboard/v1/billing/checkout?plan=starter&method=card&currency=USD",
      ),
    );
  });

  it("retains M-Pesa checkout in Ksh", async () => {
    const user = userEvent.setup();
    renderApp(<Billing />, { route: "/app/billing" });
    await user.click(await screen.findByRole("button", { name: "M-Pesa" }));
    await user.click(screen.getByRole("button", { name: "Upgrade to Starter" }));
    await waitFor(() =>
      expect(calls).toContain(
        "/api/dashboard/v1/billing/checkout?plan=starter&method=mpesa&currency=KES",
      ),
    );
  });

  it("uses an existing subscription's currency for upgrades", async () => {
    subscription = {
      currency: "USD",
      status: "active",
      current_period_end: null,
      billing_mode: "auto",
    };
    const user = userEvent.setup();
    renderApp(<Billing />, { route: "/app/billing" });
    expect(
      await screen.findByText(/Your subscription is billed in US dollars/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Billing currency" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upgrade to Business" }));
    await waitFor(() =>
      expect(calls).toContain(
        "/api/dashboard/v1/billing/checkout?plan=business&method=card&currency=USD",
      ),
    );
  });

  it("does not offer checkout when prices cannot be loaded", async () => {
    plansFailed = true;
    renderApp(<Billing />, { route: "/app/billing" });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Billing is temporarily unavailable",
    );
    expect(screen.queryByRole("button", { name: "Upgrade to Starter" })).not.toBeInTheDocument();
  });
});
