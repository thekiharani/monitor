import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatDate(value: string | null | undefined, timeZone?: string): string {
  if (!value) return "—";
  try {
    return new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
      year: "numeric",
      timeZone: timeZone || undefined,
    }).format(new Date(value));
  } catch {
    return value;
  }
}

export function formatDateTime(value: string | null | undefined, timeZone?: string): string {
  if (!value) return "—";
  try {
    return new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      timeZone: timeZone || undefined,
    }).format(new Date(value));
  } catch {
    return value;
  }
}
