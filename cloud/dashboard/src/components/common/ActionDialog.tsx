import {
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
  useRef,
  useEffect,
} from "react";
import {
  AlertTriangle,
  CheckCircle2,
  Info,
  Loader2,
  RotateCcw,
  ShieldAlert,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Slideover,
  SlideoverBody,
  SlideoverContent,
  SlideoverFooter,
  SlideoverHeader,
  type SlideoverSize,
} from "@/components/ui/slideover";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export type ActionDialogTone = "danger" | "warning" | "primary" | "success" | "neutral";

export interface TargetDetail {
  label: string;
  value: ReactNode;
  mono?: boolean;
  className?: string;
}

export interface ActionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenChangeComplete?: (open: boolean) => void;
  title: string;
  size?: SlideoverSize;
  width?: CSSProperties["width"];
  description?: ReactNode;
  icon?: LucideIcon;
  tone?: ActionDialogTone;
  targetDetails?: TargetDetail[];
  children?: ReactNode;
  requireReason?: boolean;
  reasonLabel?: string;
  reasonPlaceholder?: string;
  defaultReason?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  isLoading?: boolean;
  onConfirm: (reason: string) => void | Promise<void>;
}

const defaultIcons: Record<ActionDialogTone, LucideIcon> = {
  danger: ShieldAlert,
  warning: AlertTriangle,
  primary: RotateCcw,
  success: CheckCircle2,
  neutral: Info,
};

const toneStyles: Record<
  ActionDialogTone,
  {
    iconBox: string;
    buttonVariant: "destructive" | "default" | "secondary" | "outline";
    buttonClass?: string;
  }
> = {
  danger: {
    iconBox: "bg-destructive/10 text-destructive ring-1 ring-destructive/20",
    buttonVariant: "destructive",
    buttonClass: "bg-destructive text-destructive-foreground hover:bg-destructive/90",
  },
  warning: {
    iconBox: "bg-warning/15 text-warning ring-1 ring-warning/25",
    buttonVariant: "default",
    buttonClass: "bg-warning text-warning-foreground hover:bg-warning/90",
  },
  primary: {
    iconBox: "bg-primary/10 text-primary ring-1 ring-primary/20",
    buttonVariant: "default",
    buttonClass: "bg-primary text-primary-foreground hover:bg-primary/90",
  },
  success: {
    iconBox: "bg-success/15 text-success ring-1 ring-success/25",
    buttonVariant: "default",
    buttonClass: "bg-success text-success-foreground hover:bg-success/90",
  },
  neutral: {
    iconBox: "bg-muted text-muted-foreground ring-1 ring-border/80",
    buttonVariant: "secondary",
  },
};

export function ActionDialog({
  open,
  onOpenChange,
  onOpenChangeComplete,
  title,
  size = "md",
  width,
  description,
  icon,
  tone = "primary",
  targetDetails,
  children,
  requireReason = false,
  reasonLabel = "Auditable Reason",
  reasonPlaceholder = "Explain the reason for this operator action…",
  defaultReason = "",
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  isLoading = false,
  onConfirm,
}: ActionDialogProps) {
  const [reason, setReason] = useState(defaultReason);
  const [error, setError] = useState<string | null>(null);

  const reasonRef = useRef<HTMLInputElement>(null);
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setReason(defaultReason);
      setError(null);
    }
  }

  useEffect(() => {
    if (open) reasonRef.current?.focus();
  }, [open]);

  const IconComponent = icon || defaultIcons[tone];
  const { iconBox, buttonVariant, buttonClass } = toneStyles[tone];

  const handleSubmit = (e?: FormEvent) => {
    if (e) e.preventDefault();
    if (isLoading) return;
    const trimmed = reason.trim();
    if (requireReason && trimmed.length < 3) {
      setError("Please provide an auditable reason of at least 3 characters.");
      return;
    }
    setError(null);
    void onConfirm(trimmed);
  };

  return (
    <Slideover
      open={open}
      onOpenChange={(next) => !isLoading && onOpenChange(next)}
      onOpenChangeComplete={onOpenChangeComplete}
    >
      <SlideoverContent size={size} width={width}>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <SlideoverHeader
            title={title}
            description={description}
            icon={IconComponent}
            iconClassName={iconBox}
            showCloseButton={!isLoading}
          />
          <SlideoverBody className="space-y-6">
            {targetDetails && targetDetails.length > 0 && (
              <div className="divide-y divide-border rounded-lg border border-border bg-card p-4 text-sm">
                {targetDetails.map((item) => (
                  <div
                    key={item.label}
                    className={cn(
                      "flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0",
                      item.className,
                    )}
                  >
                    <span className="text-muted-foreground">{item.label}</span>
                    <span
                      className={cn(
                        "font-semibold text-foreground truncate max-w-[220px]",
                        item.mono && "font-mono text-2xs",
                      )}
                    >
                      {item.value}
                    </span>
                  </div>
                ))}
              </div>
            )}

            {children}

            {requireReason && (
              <div className="space-y-1.5">
                <div className="flex items-center justify-between">
                  <Label htmlFor="action-reason-input" className="text-xs font-semibold">
                    {reasonLabel}
                  </Label>
                  <span className="text-3xs text-muted-foreground">Recorded in audit log</span>
                </div>
                <Input
                  id="action-reason-input"
                  className={cn(
                    "text-sm",
                    error && "border-destructive focus-visible:ring-destructive/30",
                  )}
                  placeholder={reasonPlaceholder}
                  value={reason}
                  onChange={(e) => {
                    setReason(e.target.value);
                    if (error) setError(null);
                  }}
                  disabled={isLoading}
                  ref={reasonRef}
                />
                {error && <p className="text-2xs text-destructive">{error}</p>}
              </div>
            )}
          </SlideoverBody>
          <SlideoverFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isLoading}
            >
              {cancelLabel}
            </Button>
            <Button
              type="submit"
              variant={buttonVariant}
              className={cn("font-medium", buttonClass)}
              disabled={isLoading || (requireReason && reason.trim().length < 3)}
            >
              {isLoading && <Loader2 className="mr-1.5 size-3.5 animate-spin" />}
              {confirmLabel}
            </Button>
          </SlideoverFooter>
        </form>
      </SlideoverContent>
    </Slideover>
  );
}
