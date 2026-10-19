import { act, renderHook, screen } from "@testing-library/react";
import type { CSSProperties } from "react";
import { describe, expect, it } from "vitest";
import {
  Slideover,
  SlideoverContent,
  SlideoverHeader,
  useSlideoverState,
  type SlideoverSize,
} from "@/components/ui/slideover";
import { renderApp } from "@tests/render";

describe("Slide-over widths", () => {
  it.each<[SlideoverSize, string]>([
    ["sm", "24rem"],
    ["md", "30rem"],
    ["lg", "36rem"],
    ["xl", "42rem"],
    ["2xl", "48rem"],
    ["3xl", "56rem"],
    ["4xl", "64rem"],
    ["full", "100%"],
  ])("supports the %s size", async (size, width) => {
    renderApp(
      <Slideover open onOpenChange={() => {}}>
        <SlideoverContent size={size}>
          <SlideoverHeader title="Panel" />
        </SlideoverContent>
      </Slideover>,
    );
    expect(await screen.findByRole("dialog")).toHaveStyle({ "--sheet-max-width": width });
  });

  it.each<[CSSProperties["width"], string]>([
    [920, "920px"],
    ["72rem", "72rem"],
    ["80vw", "80vw"],
    ["min(90vw, 70rem)", "min(90vw, 70rem)"],
  ])("allows a custom width of %s to override its size preset", async (width, expected) => {
    renderApp(
      <Slideover open onOpenChange={() => {}}>
        <SlideoverContent size="sm" width={width}>
          <SlideoverHeader title="Panel" />
        </SlideoverContent>
      </Slideover>,
    );
    const panel = await screen.findByRole("dialog");
    expect(panel).toHaveStyle({ "--sheet-max-width": expected });
    expect(panel.style.maxWidth).toBe("");
  });
});

describe("Slide-over transition state", () => {
  it("retains selected content until the closing transition completes", () => {
    const { result } = renderHook(() =>
      useSlideoverState<{ id: string; name: string } | null>(null),
    );
    act(() => result.current[1]({ id: "one", name: "Production" }));
    expect(result.current[2]).toBe(true);
    act(() => result.current[1](null));
    expect(result.current[2]).toBe(false);
    expect(result.current[0]).toEqual({ id: "one", name: "Production" });
    act(() => result.current[3](false));
    expect(result.current[0]).toBeNull();
  });

  it("does not discard a reopened panel when an earlier close completes", () => {
    const { result } = renderHook(() => useSlideoverState<string | null>(null));
    act(() => result.current[1]("First"));
    act(() => result.current[1](null));
    act(() => result.current[1]("Second"));
    act(() => result.current[3](false));
    expect(result.current[0]).toBe("Second");
    expect(result.current[2]).toBe(true);
  });

  it("supports editing a selected form value before closing", () => {
    const { result } = renderHook(() => useSlideoverState<{ name: string } | null>(null));
    act(() => result.current[1]({ name: "Original" }));
    act(() =>
      result.current[1]((previous) => (previous ? { ...previous, name: "Updated" } : previous)),
    );
    act(() => result.current[1](null));
    expect(result.current[0]?.name).toBe("Updated");
    act(() => result.current[3](true));
    expect(result.current[0]?.name).toBe("Updated");
    act(() => result.current[3](false));
    expect(result.current[0]).toBeNull();
  });

  it("retains boolean-controlled panels until their exit has finished", () => {
    const { result } = renderHook(() => useSlideoverState(false));
    act(() => result.current[1](true));
    act(() => result.current[1](false));
    expect(result.current[0]).toBe(true);
    expect(result.current[2]).toBe(false);
    act(() => result.current[3](false));
    expect(result.current[0]).toBe(false);
  });
});
