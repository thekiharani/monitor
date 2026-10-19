import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { ThemeDropdown } from "@/components/layout/ThemeDropdown";

const account = {
  id: "0192f0c1-3b2a-7000-8000-0242ac120002",
  email: "operator@norialabs.com",
  plan: "free",
  status: "active",
};

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return {
    ...actual,
    api: vi.fn((path: string) => {
      if (path === actual.endpoints.me) {
        return Promise.resolve({ data: { account } });
      }
      return Promise.resolve({ data: [] });
    }),
  };
});

function trigger() {
  return screen.getByRole("button", { name: /^Appearance:/ });
}

describe("ThemeDropdown", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
  });

  it("applies and remembers the dark mode chosen from the menu", async () => {
    const user = userEvent.setup();
    renderApp(<ThemeDropdown />);

    await user.click(trigger());
    await user.click(await screen.findByRole("menuitemradio", { name: "Dark" }));

    expect(document.documentElement).toHaveClass("dark");
    expect(localStorage.getItem("noria-monitor-theme")).toBe("dark");
    expect(trigger()).toHaveAccessibleName("Appearance: Dark");
  });

  it("restores the stored mode and lets it be switched back to light", async () => {
    const user = userEvent.setup();
    localStorage.setItem("noria-monitor-theme", "dark");
    renderApp(<ThemeDropdown />);

    expect(document.documentElement).toHaveClass("dark");

    await user.click(trigger());
    await user.click(await screen.findByRole("menuitemradio", { name: "Light" }));

    expect(document.documentElement).not.toHaveClass("dark");
    expect(localStorage.getItem("noria-monitor-theme")).toBe("light");
    expect(trigger()).toHaveAccessibleName("Appearance: Light");
  });

  it("offers light, dark, and system in every menu", async () => {
    const user = userEvent.setup();
    renderApp(<ThemeDropdown />);

    await user.click(trigger());
    const menu = await screen.findByRole("menu");

    expect(
      within(menu)
        .getAllByRole("menuitemradio")
        .map((item) => item.textContent),
    ).toEqual(["Light", "Dark", "System"]);
  });
});

describe("theme switching reaches every surface", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
  });

  it("puts the same control on the sign-in page", async () => {
    const { Login } = await import("@/pages/Login");
    renderApp(<Login />, { route: "/login" });
    expect(await screen.findByRole("button", { name: /^Appearance:/ })).toBeInTheDocument();
  });

  it("puts the same control on the standalone error page", async () => {
    const { ErrorPage } = await import("@/components/common/ErrorPage");
    renderApp(<ErrorPage code={404} />, { route: "/nowhere" });
    expect(await screen.findByRole("button", { name: /^Appearance:/ })).toBeInTheDocument();
  });

  it("puts the same control in the account settings", async () => {
    const { Account } = await import("@/pages/Account");
    renderApp(<Account />, { route: "/app/account" });
    expect(await screen.findByRole("button", { name: /^Appearance:/ })).toBeInTheDocument();
  });
});
