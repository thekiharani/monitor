import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { MobileTabBar } from "@/components/layout/MobileTabBar";
import {
  controlNav,
  controlTabBar,
  customerTabBar,
  navFor,
  secondaryNav,
} from "@/components/layout/navigation";

function sheet() {
  return screen.getByRole("dialog");
}

describe("MobileTabBar", () => {
  it("gives the customer workspace a create button beside its tabs", () => {
    renderApp(
      <MobileTabBar
        workspace="customer"
        destinations={navFor("owner")}
        tabDestinations={customerTabBar}
        secondary={secondaryNav}
        canAccessControl={false}
        onCreateMonitor={() => {}}
        onSignOut={() => {}}
      />,
      { route: "/app" },
    );

    const bar = screen.getByRole("navigation", { name: "Primary" });
    expect(within(bar).getByRole("button", { name: "Add monitor" })).toBeInTheDocument();
    for (const label of ["Overview", "Monitors", "Incidents"]) {
      expect(within(bar).getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  it("reaches every remaining customer destination through More", async () => {
    const user = userEvent.setup();
    renderApp(
      <MobileTabBar
        workspace="customer"
        destinations={navFor("owner")}
        tabDestinations={customerTabBar}
        secondary={secondaryNav}
        canAccessControl
        onCreateMonitor={() => {}}
        onSignOut={() => {}}
      />,
      { route: "/app" },
    );

    await user.click(screen.getByRole("button", { name: "More destinations" }));

    for (const label of ["Alerts", "Team", "Usage", "API Keys", "Billing"]) {
      expect(within(sheet()).getByRole("link", { name: label })).toBeInTheDocument();
    }
    expect(within(sheet()).getByRole("link", { name: "Control plane" })).toBeInTheDocument();
    expect(within(sheet()).getByRole("link", { name: "Account settings" })).toBeInTheDocument();
  });

  it("gives the control plane the same tab bar without a create button", async () => {
    const user = userEvent.setup();
    renderApp(
      <MobileTabBar
        workspace="control"
        destinations={controlNav}
        tabDestinations={controlTabBar}
        canAccessControl
        onSignOut={() => {}}
      />,
      { route: "/control/overview" },
    );

    const bar = screen.getByRole("navigation", { name: "Primary" });
    expect(within(bar).queryByRole("button", { name: "Add monitor" })).not.toBeInTheDocument();
    for (const label of ["Overview", "Accounts", "Billing", "Audit trail"]) {
      expect(within(bar).getByRole("link", { name: label })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("button", { name: "More destinations" }));

    for (const label of ["Team access", "Credentials", "Access grants"]) {
      expect(within(sheet()).getByRole("link", { name: label })).toBeInTheDocument();
    }
    expect(within(sheet()).getByRole("link", { name: "Customer dashboard" })).toBeInTheDocument();
  });

  it("offers only the operator sections the capabilities allow", async () => {
    const user = userEvent.setup();
    const readOnly = controlNav.filter(
      (item) => !item.capability || item.capability === "platform.accounts.read",
    );
    renderApp(
      <MobileTabBar
        workspace="control"
        destinations={readOnly}
        tabDestinations={controlTabBar}
        canAccessControl
        onSignOut={() => {}}
      />,
      { route: "/control/overview" },
    );

    await user.click(screen.getByRole("button", { name: "More destinations" }));

    expect(within(sheet()).getByRole("link", { name: "Lifecycle" })).toBeInTheDocument();
    expect(within(sheet()).queryByRole("link", { name: "Billing" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Audit trail" })).not.toBeInTheDocument();
  });

  it("signs out from the More sheet", async () => {
    const user = userEvent.setup();
    const onSignOut = vi.fn();
    renderApp(
      <MobileTabBar
        workspace="control"
        destinations={controlNav}
        tabDestinations={controlTabBar}
        canAccessControl
        onSignOut={onSignOut}
      />,
      { route: "/control/overview" },
    );

    await user.click(screen.getByRole("button", { name: "More destinations" }));
    await user.click(within(sheet()).getByRole("button", { name: "Sign out" }));

    expect(onSignOut).toHaveBeenCalledOnce();
  });
});
