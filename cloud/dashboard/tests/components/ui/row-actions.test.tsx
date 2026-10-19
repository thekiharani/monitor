import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router-dom";
import { Copy, Eye, Trash2 } from "lucide-react";
import { RowActions, copyRowValue } from "@/components/ui/row-actions";
import { renderApp } from "@tests/render";

function LocationState() {
  const location = useLocation();
  return (
    <output aria-label="Location">
      {location.pathname} {JSON.stringify(location.state)}
    </output>
  );
}

describe("Grouped row actions", () => {
  it("opens a labeled menu, separates destructive actions, and restores focus on Escape", async () => {
    const user = userEvent.setup();
    renderApp(
      <RowActions
        label="Actions for Production"
        items={[
          { key: "view", label: "View", icon: Eye, onClick: vi.fn() },
          { key: "copy", label: "Copy reference", icon: Copy, onClick: vi.fn() },
          {
            key: "remove",
            label: "Delete",
            icon: Trash2,
            variant: "destructive",
            separator: true,
            onClick: vi.fn(),
          },
        ]}
      />,
    );
    const trigger = screen.getByRole("button", { name: "Actions for Production" });
    await user.click(trigger);
    const menu = await screen.findByRole("menu");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["View", "Copy reference", "Delete"]);
    expect(within(menu).getByRole("separator")).toBeInTheDocument();
    expect(within(menu).getByRole("menuitem", { name: "Delete" })).toHaveAttribute(
      "data-variant",
      "destructive",
    );
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("keeps disabled actions inert and invokes an enabled action once", async () => {
    const user = userEvent.setup();
    const disabled = vi.fn();
    const enabled = vi.fn();
    renderApp(
      <RowActions
        items={[
          { key: "disabled", label: "Retry", disabled: true, onClick: disabled },
          { key: "enabled", label: "View", onClick: enabled },
        ]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Row actions" }));
    const retry = await screen.findByRole("menuitem", { name: "Retry" });
    expect(retry).toHaveAttribute("aria-disabled", "true");
    await user.click(retry);
    expect(disabled).not.toHaveBeenCalled();
    await user.click(screen.getByRole("menuitem", { name: "View" }));
    expect(enabled).toHaveBeenCalledTimes(1);
  });

  it("uses real links and preserves navigation state for slide-over routes", async () => {
    const user = userEvent.setup();
    renderApp(
      <>
        <RowActions
          items={[
            { key: "view", label: "View", to: "/app/monitors/one/edit", state: { from: "table" } },
          ]}
        />
        <LocationState />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Row actions" }));
    const link = await screen.findByRole("menuitem", { name: "View" });
    expect(link).toHaveAttribute("href", "/app/monitors/one/edit");
    await user.click(link);
    expect(screen.getByLabelText("Location")).toHaveTextContent(
      '/app/monitors/one/edit {"from":"table"}',
    );
  });

  it("does not render empty menus", () => {
    renderApp(<RowActions items={[]} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("copies references and handles clipboard failures without rejecting", async () => {
    userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    await copyRowValue("reference-123");
    expect(write).toHaveBeenCalledWith("reference-123");
    write.mockRejectedValueOnce(new Error("Clipboard unavailable"));
    await expect(copyRowValue("reference-456")).resolves.toBeUndefined();
  });
});
