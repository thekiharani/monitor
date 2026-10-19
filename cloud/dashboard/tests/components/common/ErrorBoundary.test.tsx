import type React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ThemeProvider } from "@/context/theme-context";
import { ErrorBoundary, RootErrorFallback } from "@/components/common/ErrorBoundary";

function Boom(): never {
  throw new Error("render exploded");
}

function wrap(ui: React.ReactNode) {
  return (
    <ThemeProvider defaultTheme="light">
      <MemoryRouter>{ui}</MemoryRouter>
    </ThemeProvider>
  );
}

describe("ErrorBoundary", () => {
  it("renders a recoverable page instead of blanking the app", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      wrap(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      ),
    );

    expect(document.body.textContent).not.toBe("");
    expect(screen.getAllByText(/500/).length).toBeGreaterThan(0);
    expect(screen.queryByText("render exploded")).not.toBeInTheDocument();
  });

  it("uses a provider-free fallback when asked, for failures in the providers", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      <ErrorBoundary fallback={<RootErrorFallback />}>
        <Boom />
      </ErrorBoundary>,
    );

    expect(screen.getByRole("heading", { name: /failed to start/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /reload/i })).toBeInTheDocument();
  });

  it("passes children through when nothing throws", () => {
    render(
      wrap(
        <ErrorBoundary>
          <p>healthy</p>
        </ErrorBoundary>,
      ),
    );
    expect(screen.getByText("healthy")).toBeInTheDocument();
  });

  it("clears the error when the reset key changes, so navigation recovers", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    const { rerender } = render(
      wrap(
        <ErrorBoundary resetKey="/broken">
          <Boom />
        </ErrorBoundary>,
      ),
    );
    expect(screen.getAllByText(/500/).length).toBeGreaterThan(0);

    rerender(
      wrap(
        <ErrorBoundary resetKey="/somewhere-else">
          <p>recovered</p>
        </ErrorBoundary>,
      ),
    );
    expect(screen.getByText("recovered")).toBeInTheDocument();
  });
});
