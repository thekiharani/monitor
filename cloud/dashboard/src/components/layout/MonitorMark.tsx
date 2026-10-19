import { cn } from "@/lib/utils";

export const MONITOR_MARK_PULSE = "M6 34H19L25 20L33 46L40 28L44 34H58";
export const MONITOR_MARK_STROKE = 6;

export function MonitorMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 64 64"
      className={cn("size-full", className)}
      role="img"
      aria-label="Noria Monitor"
    >
      <path
        d={MONITOR_MARK_PULSE}
        fill="none"
        stroke="currentColor"
        strokeWidth={MONITOR_MARK_STROKE}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
