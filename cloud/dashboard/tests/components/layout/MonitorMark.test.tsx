import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render } from "@testing-library/react";
import { MONITOR_MARK_PULSE, MonitorMark } from "@/components/layout/MonitorMark";

const publicDir = resolve(__dirname, "../../../public");
const faviconSvg = readFileSync(resolve(publicDir, "favicon.svg"), "utf8");
const manifest = JSON.parse(readFileSync(resolve(publicDir, "site.webmanifest"), "utf8"));
const indexHtml = readFileSync(resolve(__dirname, "../../../index.html"), "utf8");

describe("Monitor mark", () => {
  it("draws the pulse line in the current colour", () => {
    const { container } = render(<MonitorMark />);
    const pulse = container.querySelector("path");

    expect(container.querySelector("svg")).toHaveAccessibleName("Noria Monitor");
    expect(pulse?.getAttribute("d")).toBe(MONITOR_MARK_PULSE);
    expect(pulse?.getAttribute("stroke")).toBe("currentColor");
  });

  it("paints browser chrome and the install splash from the app palette", () => {
    expect(manifest.background_color).toBe("#f4fcfc");
    expect(manifest.theme_color).toBe("#009498");
    expect(manifest.icons.some((icon: { purpose?: string }) => icon.purpose === "maskable")).toBe(
      true,
    );

    expect(indexHtml).toContain(
      '<meta name="theme-color" content="#f4fcfc" media="(prefers-color-scheme: light)" />',
    );
    expect(indexHtml).toContain(
      '<meta name="theme-color" content="#000709" media="(prefers-color-scheme: dark)" />',
    );
  });

  it("keeps the retired cyan out of the brand assets", () => {
    for (const source of [faviconSvg, JSON.stringify(manifest), indexHtml]) {
      expect(source).not.toContain("#0891b2");
      expect(source).not.toContain("#67e8f9");
      expect(source).not.toContain("#f9f8fc");
    }
  });
});
