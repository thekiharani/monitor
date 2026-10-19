import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TimezoneProvider, useDateFormat } from "@/context/timezone-context";
import { formatDate, formatDateTime } from "@/lib/utils";

const NEW_YEAR_UTC = "2026-01-01T02:30:00Z";

function Stamp() {
  const format = useDateFormat();
  return <p>{format.formatDateTime(NEW_YEAR_UTC)}</p>;
}

describe("preferred time zone", () => {
  it("moves a timestamp onto the chosen zone's calendar day", () => {
    expect(formatDate(NEW_YEAR_UTC, "UTC")).toContain("Jan 1");
    expect(formatDate(NEW_YEAR_UTC, "America/New_York")).toContain("Dec 31");
  });

  it("renders the clock in the chosen zone", () => {
    const nairobi = formatDateTime(NEW_YEAR_UTC, "Africa/Nairobi");
    const utc = formatDateTime(NEW_YEAR_UTC, "UTC");
    expect(nairobi).toContain("05:30");
    expect(utc).toContain("02:30");
  });

  it("falls back to the browser zone when no preference is set", () => {
    const browser = new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
      year: "numeric",
    }).format(new Date(NEW_YEAR_UTC));
    expect(formatDate(NEW_YEAR_UTC, "")).toBe(browser);
    expect(formatDate(NEW_YEAR_UTC)).toBe(browser);
  });

  it("hands the provider's zone to everything it wraps", () => {
    render(
      <TimezoneProvider timezone="Asia/Tokyo">
        <Stamp />
      </TimezoneProvider>,
    );
    expect(screen.getByText(/11:30/)).toBeInTheDocument();
  });
});
