import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { ControlRowActions } from "@/pages/control/shared";
import type { ControlSession } from "@/control-api";

const session: ControlSession = {
  principal_id: "operator",
  account_id: "account",
  email: "ops@example.test",
  capabilities: [],
  known_capabilities: [],
};
const row = {
  id: "record-1",
  account_id: "account-1",
  name: "Production credential",
  reason: "The full reason for this record",
  secret_hash: "must-not-be-rendered",
  metadata: { access_token: "nested-secret", region: "eu-west" },
};

describe("Control record menus", () => {
  it("opens full record details without exposing secret fields", async () => {
    const user = userEvent.setup();
    renderApp(<ControlRowActions row={row} session={session} />);
    await user.click(screen.getByRole("button", { name: "Actions for Production credential" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).queryByRole("menuitem", { name: "View account" })).not.toBeInTheDocument();
    await user.click(within(menu).getByRole("menuitem", { name: "View" }));
    const panel = await screen.findByRole("dialog", { name: "Record details" });
    expect(within(panel).getByText("The full reason for this record")).toBeInTheDocument();
    expect(within(panel).getByText("Redacted")).toBeInTheDocument();
    expect(screen.queryByText("must-not-be-rendered")).not.toBeInTheDocument();
    expect(within(panel).getByText(/"access_token": "Redacted"/)).toHaveTextContent(
      '"region": "eu-west"',
    );
    expect(screen.queryByText(/nested-secret/)).not.toBeInTheDocument();
  });

  it("offers account navigation only to operators with the account-read capability", async () => {
    const user = userEvent.setup();
    renderApp(
      <ControlRowActions
        row={row}
        session={{ ...session, capabilities: ["platform.accounts.read"] }}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions for Production credential" }));
    expect(await screen.findByRole("menuitem", { name: "View account" })).toHaveAttribute(
      "href",
      "/control/accounts/account-1",
    );
  });
});
