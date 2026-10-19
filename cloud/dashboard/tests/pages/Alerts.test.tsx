import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { ChannelForm } from "@/pages/Alerts";

const calls: { path: string; method: string; body: unknown }[] = [];

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ path, method, body });
      if (path === actual.endpoints.alertChannels() && method === "POST") {
        return Promise.resolve({
          data: { id: "c-1", ...(body as object), config: {}, monitor_ids: [] },
        });
      }
      return Promise.resolve({ data: [] });
    }),
  };
});

describe("Channel form", () => {
  it("switches fields with the kind and sends only what that kind needs", async () => {
    const user = userEvent.setup();
    const saved = vi.fn();
    renderApp(<ChannelForm telegramAvailable={false} telegramBot="" onSaved={saved} />);

    expect(screen.getByLabelText("Email addresses")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Telegram/ })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: /Webhook/ }));
    expect(screen.queryByLabelText("Email addresses")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Name"), "Ops hook");
    await user.type(screen.getByLabelText("Endpoint URL"), "https://example.com/hook");
    await user.click(screen.getByRole("button", { name: "Add channel" }));

    await waitFor(() => expect(saved).toHaveBeenCalled());
    expect(calls.find((call) => call.method === "POST")?.body).toEqual({
      kind: "webhook",
      name: "Ops hook",
      url: "https://example.com/hook",
      all_monitors: true,
      monitor_ids: [],
    });
  });

  it("refuses a scoped channel with no monitors", async () => {
    const user = userEvent.setup();
    renderApp(<ChannelForm telegramAvailable telegramBot="noria_monitor_bot" onSaved={vi.fn()} />);
    await user.type(screen.getByLabelText("Name"), "Ops");
    await user.type(screen.getByLabelText("Email addresses"), "ops@example.com");
    await user.click(screen.getByRole("switch", { name: /Alert for every monitor/ }));
    await user.click(screen.getByRole("button", { name: "Add channel" }));
    expect(await screen.findByText(/Pick at least one monitor/)).toBeInTheDocument();
  });
});
