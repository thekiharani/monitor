import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { CommandMenu } from "@/components/common/CommandMenu";

vi.mock("@/api", async () => {
  const actual = await vi.importActual<typeof import("@/api")>("@/api");
  return { ...actual, api: vi.fn(() => Promise.resolve({ data: [] })) };
});

function selectedItem() {
  return document
    .querySelector('[data-slot=command-item][aria-selected="true"]')
    ?.textContent?.trim();
}

describe("CommandMenu", () => {
  it("filters as you type", async () => {
    const user = userEvent.setup();
    renderApp(<CommandMenu open onOpenChange={() => {}} />);

    expect(screen.getByText("Billing")).toBeInTheDocument();

    await user.type(screen.getByRole("combobox"), "usage");

    await waitFor(() => expect(screen.queryByText("Billing")).not.toBeInTheDocument());
    expect(screen.getByText("Usage")).toBeInTheDocument();
  });

  it("moves the selection with the arrow keys and runs it on Enter", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderApp(<CommandMenu open onOpenChange={onOpenChange} onOpenCreateMonitor={() => {}} />);

    expect(selectedItem()).toBe("Overview");

    await user.click(screen.getByRole("combobox"));
    await user.keyboard("{ArrowDown}");
    expect(selectedItem()).toBe("Monitors");

    await user.keyboard("{ArrowDown}");
    expect(selectedItem()).toBe("Add monitor");

    await user.keyboard("{Enter}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
