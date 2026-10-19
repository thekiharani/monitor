import { ChevronDown, Monitor, Moon, Sun } from "lucide-react";
import { useTheme, type Theme } from "@/context/theme-context";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const options: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "system", label: "System", icon: Monitor },
];

export function ThemeDropdown({
  className,
  align = "end",
}: {
  className?: string;
  align?: "start" | "center" | "end";
}) {
  const { theme, setTheme } = useTheme();
  const selected = options.find((option) => option.value === theme) ?? options[2]!;
  const SelectedIcon = selected.icon;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="outline" size="lg" />}
        className={cn("gap-2 rounded-xl", className)}
        aria-label={`Appearance: ${selected.label}`}
      >
        <SelectedIcon className="size-4 text-muted-foreground" />
        <span className="hidden sm:inline">{selected.label}</span>
        <ChevronDown className="size-3 text-muted-foreground opacity-70" />
      </DropdownMenuTrigger>

      <DropdownMenuContent align={align} className="w-44">
        <DropdownMenuLabel>Appearance</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup value={theme} onValueChange={(next) => setTheme(next as Theme)}>
          {options.map((option) => {
            const Icon = option.icon;
            return (
              <DropdownMenuRadioItem key={option.value} value={option.value}>
                <Icon className="size-4 text-muted-foreground" />
                <span>{option.label}</span>
              </DropdownMenuRadioItem>
            );
          })}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
