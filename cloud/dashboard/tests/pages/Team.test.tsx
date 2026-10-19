import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { Team } from "@/pages/Team";
import { api, endpoints } from "@/api";

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((_path: string, init?: RequestInit) =>
      Promise.resolve(
        init?.method === "POST"
          ? { data: { id: "invite-1" } }
          : {
              data: {
                members: [],
                invites: [],
                limit: 5,
                seats_taken: 1,
                your_role: "owner",
                can_manage: true,
              },
            },
      ),
    ),
  };
});

describe("Team invitation slide-over", () => {
  it("keeps the invite form out of the list until requested and closes after sending", async () => {
    const user = userEvent.setup();
    renderApp(<Team />);
    await screen.findByRole("button", { name: "Invite teammate" });
    expect(screen.queryByLabelText("Teammate email")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Invite teammate" }));
    const panel = await screen.findByRole("dialog", { name: "Invite a teammate" });
    await user.type(within(panel).getByLabelText("Teammate email"), "ops@example.com");
    await user.click(within(panel).getByRole("button", { name: "Send invite" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api).toHaveBeenCalledWith(endpoints.memberInvites(), {
      method: "POST",
      body: JSON.stringify({ email: "ops@example.com", role: "member" }),
    });
  });
});
