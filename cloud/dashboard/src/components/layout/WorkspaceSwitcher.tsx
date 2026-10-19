import { useNavigate } from "react-router-dom";
import { crossWorkspaceDestination } from "@/components/layout/navigation";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function WorkspaceSwitcher({
  workspace,
  canAccessControl,
  className,
}: {
  workspace: "customer" | "control";
  canAccessControl: boolean;
  className?: string;
}) {
  const navigate = useNavigate();
  const destination = crossWorkspaceDestination(workspace, canAccessControl);

  if (!destination) return null;

  const Icon = destination.icon;

  return (
    <Button
      variant="outline"
      aria-label={destination.label}
      className={cn(
        "h-9 shrink-0 gap-2 rounded-xl px-3 text-xs font-semibold max-md:w-9 max-md:px-0",
        className,
      )}
      onClick={() => navigate(destination.to)}
    >
      <Icon className="size-3.5" />
      <span className="hidden md:inline">{destination.label}</span>
    </Button>
  );
}
