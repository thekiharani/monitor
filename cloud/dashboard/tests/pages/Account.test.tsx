import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { Account } from "@/pages/Account";

const account = {
  id: "0192f0c1-3b2a-7000-8000-0242ac120002",
  email: "operator@norialabs.com",
  plan: "free",
  status: "active",
  role: "owner",
  name: "",
  display_name: "",
  timezone: "",
};

let current = { ...account };
const calls: { path: string; method: string; body: unknown }[] = [];

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, method, body });
      if (path === actual.endpoints.me && method === "PATCH") {
        current = { ...current, ...(body as object) };
        return Promise.resolve({ data: { account: current } });
      }
      if (path === "/api/dashboard/v1/account" && method === "PATCH") {
        current = { ...current, ...(body as object) };
        return Promise.resolve({ data: { account: current } });
      }
      if (path === "/api/dashboard/v1/me/email" && method === "POST") {
        return Promise.resolve({ challenge_id: "challenge-1" });
      }
      if (path === "/api/dashboard/v1/me/email/verify" && method === "POST") {
        current = { ...current, email: (body as { email: string }).email };
        return Promise.resolve({ data: { account: current } });
      }
      return Promise.resolve({ data: { account: current } });
    }),
  };
});

describe("Account settings", () => {
  beforeEach(() => {
    current = { ...account };
    calls.length = 0;
  });

  it("saves a display name only once it has been changed", async () => {
    const user = userEvent.setup();
    renderApp(<Account />, { route: "/app/account" });

    const save = await screen.findByRole("button", { name: "Save profile" });
    expect(save).toBeDisabled();

    await user.type(screen.getByLabelText("Display name"), "Wanjiru Kamau");
    expect(save).toBeEnabled();
    await user.click(save);

    await waitFor(() => {
      expect(
        calls.some(
          (call) =>
            call.method === "PATCH" &&
            call.path === "/api/dashboard/v1/me" &&
            (call.body as { display_name: string }).display_name === "Wanjiru Kamau",
        ),
      ).toBe(true);
    });
  });

  it("lets an owner rename the workspace", async () => {
    const user = userEvent.setup();
    renderApp(<Account />, { route: "/app/account" });

    const field = await screen.findByLabelText("Workspace name");
    expect(field).toBeEnabled();
    await user.type(field, "Noria Labs");
    await user.click(screen.getByRole("button", { name: "Save workspace name" }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) =>
            call.path === "/api/dashboard/v1/account" &&
            (call.body as { name: string }).name === "Noria Labs",
        ),
      ).toBe(true);
    });
  });

  it("does not offer a rename to a plain member", async () => {
    current = { ...account, role: "member" };
    renderApp(<Account />, { route: "/app/account" });

    expect(await screen.findByLabelText("Workspace name")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save workspace name" })).not.toBeInTheDocument();
    expect(
      screen.getByText("Only an owner or admin can rename this workspace."),
    ).toBeInTheDocument();
  });

  it("asks for a code before moving the sign-in address", async () => {
    const user = userEvent.setup();
    renderApp(<Account />, { route: "/app/account" });

    await user.type(await screen.findByLabelText("New email address"), "new@norialabs.com");
    await user.click(screen.getByRole("button", { name: "Send confirmation code" }));

    const codeField = await screen.findByLabelText("Code sent to new@norialabs.com");
    const confirm = screen.getByRole("button", { name: "Confirm new address" });
    expect(confirm).toBeDisabled();

    await user.type(codeField, "424242");
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => {
      expect(
        calls.some(
          (call) =>
            call.path === "/api/dashboard/v1/me/email/verify" &&
            (call.body as { code: string }).code === "424242",
        ),
      ).toBe(true);
    });
  });

  it("keeps the close-account confirmation tied to the email address", async () => {
    const user = userEvent.setup();
    renderApp(<Account />, { route: "/app/account" });

    const close = await screen.findByRole("button", {
      name: "Close this account permanently",
    });
    expect(close).toBeDisabled();

    await user.type(screen.getByLabelText(/to confirm/), account.email);
    expect(close).toBeEnabled();
  });
});
