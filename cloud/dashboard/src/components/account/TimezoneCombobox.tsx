import { useMemo, useState } from "react";
import { Check, ChevronsUpDown, Globe } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

const FALLBACK_ZONES = [
  "Africa/Nairobi",
  "Africa/Lagos",
  "Africa/Johannesburg",
  "Africa/Cairo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Sao_Paulo",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "UTC",
];

export function supportedTimezones(): string[] {
  try {
    const supported = Intl.supportedValuesOf?.("timeZone");
    if (supported && supported.length > 0) return [...supported];
  } catch {
    return FALLBACK_ZONES;
  }
  return FALLBACK_ZONES;
}

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function zoneOffsetLabel(zone: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "shortOffset",
    }).formatToParts(new Date());
    return parts.find((part) => part.type === "timeZoneName")?.value ?? "";
  } catch {
    return "";
  }
}

interface TimezoneComboboxProps {
  value: string;
  onChange: (zone: string) => void;
  id?: string;
  disabled?: boolean;
  className?: string;
}

export function TimezoneCombobox({
  value,
  onChange,
  id,
  disabled,
  className,
}: TimezoneComboboxProps) {
  const [open, setOpen] = useState(false);
  const zones = useMemo(() => supportedTimezones(), []);
  const offset = value ? zoneOffsetLabel(value) : "";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={<Button variant="outline" size="lg" />}
        id={id}
        disabled={disabled}
        className={cn("w-full justify-between gap-2 rounded-xl font-normal", className)}
      >
        <Globe className="size-4 shrink-0 text-muted-foreground" />
        <span className={cn("truncate", !value && "text-muted-foreground")}>
          {value || "Use the time zone of this browser"}
        </span>
        {offset && (
          <span className="ml-auto shrink-0 font-mono text-2xs text-muted-foreground">
            {offset}
          </span>
        )}
        <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground opacity-70" />
      </PopoverTrigger>

      <PopoverContent align="start" className="w-(--anchor-width) p-0">
        <Command>
          <CommandInput placeholder="Search time zones…" />
          <CommandList className="max-h-72">
            <CommandEmpty>No time zone matches that.</CommandEmpty>
            <CommandGroup>
              <CommandItem
                value="browser default"
                onSelect={() => {
                  onChange("");
                  setOpen(false);
                }}
              >
                <Check className={cn("size-4", value ? "opacity-0" : "opacity-100")} />
                <span>Use the time zone of this browser</span>
                <span className="ml-auto font-mono text-2xs text-muted-foreground">
                  {browserTimezone()}
                </span>
              </CommandItem>
              {zones.map((zone) => (
                <CommandItem
                  key={zone}
                  value={zone}
                  onSelect={() => {
                    onChange(zone);
                    setOpen(false);
                  }}
                >
                  <Check className={cn("size-4", zone === value ? "opacity-100" : "opacity-0")} />
                  <span className="truncate">{zone.replace(/_/g, " ")}</span>
                  <span className="ml-auto shrink-0 font-mono text-2xs text-muted-foreground">
                    {zoneOffsetLabel(zone)}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
