import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";

const owner = {
  id: "0192f0c1-3b2a-7000-8000-0242ac120002",
  email: "operator@norialabs.com",
  plan: "free",
  status: "active",
  role: "owner",
};

let account: Record<string, unknown> = owner;

const usage = {
  api_requests: { used: 10, limit: 1000 },
};

const quotaExhausted = {
  api_requests: { used: 950, limit: 1000 },
};

let usagePayload = usage;

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string) => {
      if (path === actual.endpoints.me) {
        return Promise.resolve({ data: { account } });
      }
      if (path === actual.endpoints.usage) {
        return Promise.resolve({ data: usagePayload });
      }
      return Promise.resolve({ data: [] });
    }),
  };
});

vi.mock("@/control-api", async () => {
  const actual = await vi.importActual<typeof import("@/control-api")>("@/control-api");
  return { ...actual, useControlSession: () => ({ data: undefined }) };
});

describe("Shell", () => {
  beforeEach(() => {
    usagePayload = usage;
    account = owner;
  });

  it("renders every workspace destination once the account resolves", async () => {
    const { Shell } = await import("@/components/layout/Shell");
    renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    expect(await screen.findByText("Page body")).toBeInTheDocument();

    for (const label of ["Overview", "Monitors", "Team", "API Keys", "Usage", "Billing"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  it("hides the destinations a member's role cannot reach", async () => {
    account = { ...owner, role: "member" };
    const { Shell } = await import("@/components/layout/Shell");
    renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    expect(await screen.findByText("Page body")).toBeInTheDocument();

    for (const label of ["Overview", "Team", "API Keys", "Usage"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
    for (const label of ["Billing"]) {
      expect(screen.queryByRole("link", { name: label })).not.toBeInTheDocument();
    }
  });

  it("shows team and API keys but not billing to an admin", async () => {
    account = { ...owner, role: "admin" };
    const { Shell } = await import("@/components/layout/Shell");
    renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    expect(await screen.findByText("Page body")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "API Keys" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Billing" })).not.toBeInTheDocument();
  });

  it("starts expanded and collapses when the trigger is pressed", async () => {
    const user = userEvent.setup();
    const { Shell } = await import("@/components/layout/Shell");
    const { container } = renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    await screen.findByText("Page body");
    const sidebar = container.querySelector("[data-slot=sidebar]");
    expect(sidebar).toHaveAttribute("data-state", "expanded");

    await user.click(container.querySelector<HTMLElement>("[data-sidebar=trigger]")!);
    expect(sidebar).toHaveAttribute("data-state", "collapsed");
  });

  it("warns only once the free plan is near its quota, and the warning dismisses", async () => {
    const { Shell } = await import("@/components/layout/Shell");
    const { unmount } = renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    await screen.findByText("Page body");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    unmount();

    usagePayload = quotaExhausted;
    renderApp(
      <Shell>
        <p>Page body</p>
      </Shell>,
      { route: "/app" },
    );

    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  });
});
