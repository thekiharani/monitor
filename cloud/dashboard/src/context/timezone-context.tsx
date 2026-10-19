import { createContext, useContext, useMemo, type ReactNode } from "react";
import { formatDate, formatDateTime } from "@/lib/utils";

const TimezoneContext = createContext("");

export function TimezoneProvider({
  timezone,
  children,
}: {
  timezone: string;
  children: ReactNode;
}) {
  return <TimezoneContext.Provider value={timezone}>{children}</TimezoneContext.Provider>;
}

export function useTimezone(): string {
  return useContext(TimezoneContext);
}

export function useDateFormat() {
  const timezone = useTimezone();
  return useMemo(
    () => ({
      formatDate: (value: string | null | undefined) => formatDate(value, timezone),
      formatDateTime: (value: string | null | undefined) => formatDateTime(value, timezone),
    }),
    [timezone],
  );
}
