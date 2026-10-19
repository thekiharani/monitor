import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { APIKeys } from "@/pages/APIKeys";
import { api, endpoints } from "@/api";

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((_path: string, init?: RequestInit) =>
      Promise.resolve(
        init?.method === "POST"
          ? { data: { id: "key-1", name: "Production", key: "secret-only-shown-once" } }
          : { data: [] },
      ),
    ),
  };
});

describe("API key slide-over", () => {
  it("creates a key in a panel and clears its secret when dismissed", async () => {
    const user = userEvent.setup();
    renderApp(<APIKeys />);
    expect(screen.queryByLabelText("Key name")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create API key" }));
    const panel = await screen.findByRole("dialog", { name: "Create API key" });
    await user.type(within(panel).getByLabelText("Key name"), "Production");
    await user.click(within(panel).getByRole("button", { name: "Create Key" }));
    expect(await screen.findByText("secret-only-shown-once")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(endpoints.apiKeys(), {
      method: "POST",
      body: JSON.stringify({ name: "Production" }),
    });
    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByText("secret-only-shown-once")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create API key" }));
    expect(await screen.findByLabelText("Key name")).toHaveValue("");
    expect(screen.queryByText("secret-only-shown-once")).not.toBeInTheDocument();
  });
});
