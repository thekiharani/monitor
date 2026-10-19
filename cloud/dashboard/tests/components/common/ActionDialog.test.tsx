import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderApp } from "@tests/render";
import { ActionDialog } from "@/components/common/ActionDialog";

function Example() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open action</button>
      <ActionDialog
        open={open}
        onOpenChange={setOpen}
        title="Edit workspace"
        requireReason
        onConfirm={vi.fn()}
      />
    </>
  );
}

describe("Action slide-over", () => {
  it("passes configurable widths through to its slide-over", async () => {
    renderApp(
      <ActionDialog
        open
        size="3xl"
        width="60rem"
        onOpenChange={vi.fn()}
        title="Wide action"
        onConfirm={vi.fn()}
      />,
    );
    expect(await screen.findByRole("dialog", { name: "Wide action" })).toHaveStyle({
      "--sheet-max-width": "60rem",
    });
  });

  it("keeps the action in a side panel and submits a trimmed audit reason", async () => {
    const user = userEvent.setup();
    const confirm = vi.fn();
    renderApp(
      <ActionDialog
        open
        onOpenChange={vi.fn()}
        title="Change plan"
        description="Applies to this workspace."
        requireReason
        onConfirm={confirm}
      />,
    );

    const panel = await screen.findByRole("dialog", { name: "Change plan" });
    expect(panel).toHaveAttribute("data-slot", "sheet-content");
    expect(panel).toHaveAttribute("data-side", "right");
    expect(screen.getByRole("button", { name: "Confirm" })).toBeDisabled();
    await user.type(screen.getByLabelText("Auditable Reason"), "  Requested by owner  ");
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    expect(confirm).toHaveBeenCalledWith("Requested by owner");
  });

  it("ignores backdrop presses and lets Escape explicitly close the panel", async () => {
    const user = userEvent.setup();
    const change = vi.fn();
    renderApp(<ActionDialog open onOpenChange={change} title="Revoke key" onConfirm={vi.fn()} />);
    await screen.findByRole("dialog");
    const backdrop = document.querySelector('[data-slot="sheet-overlay"]')!;
    await user.click(backdrop);
    expect(change).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(change).toHaveBeenCalledWith(false);
  });

  it("blocks duplicate submissions and dismissal while an action is running", async () => {
    const user = userEvent.setup();
    const change = vi.fn();
    const confirm = vi.fn();
    renderApp(
      <ActionDialog open isLoading onOpenChange={change} title="Saving plan" onConfirm={confirm} />,
    );
    const panel = await screen.findByRole("dialog");
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Close panel" })).not.toBeInTheDocument();
    fireEvent.submit(panel.querySelector("form")!);
    await user.keyboard("{Escape}");
    expect(confirm).not.toHaveBeenCalled();
    expect(change).not.toHaveBeenCalled();
  });

  it("resets the audit reason when reopened and restores focus to its trigger", async () => {
    const user = userEvent.setup();
    renderApp(<Example />);
    const trigger = screen.getByRole("button", { name: "Open action" });
    await user.click(trigger);
    await user.type(await screen.findByLabelText("Auditable Reason"), "Old reason");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
    await user.click(trigger);
    expect(await screen.findByLabelText("Auditable Reason")).toHaveValue("");
  });
});
