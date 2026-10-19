import { Link as RouterLink } from "react-router-dom";
import { MonitorMark } from "@/components/layout/MonitorMark";
import { cn } from "@/lib/utils";

interface LogoProps {
  className?: string;
}

export function Logo({ className }: LogoProps) {
  return (
    <RouterLink
      className={cn(
        "group flex w-fit items-center gap-2 font-display text-[1.05rem] font-semibold text-foreground",
        className,
      )}
      to="/app"
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-md bg-foreground p-1.5 text-background">
        <MonitorMark />
      </span>
      <span className="truncate">
        Noria <span className="font-normal text-muted-foreground">Monitor</span>
      </span>
    </RouterLink>
  );
}
