import {
  useCallback,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
  type SetStateAction,
} from "react";
import { X, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { cn } from "@/lib/utils";

export type SlideoverSize = "sm" | "md" | "lg" | "xl" | "2xl" | "3xl" | "4xl" | "full";

const sizeWidths: Record<SlideoverSize, string> = {
  sm: "24rem",
  md: "30rem",
  lg: "36rem",
  xl: "42rem",
  "2xl": "48rem",
  "3xl": "56rem",
  "4xl": "64rem",
  full: "100%",
};

export function useSlideoverState<T>(initial: T) {
  const [state, setState] = useState({ value: initial, retained: initial, open: Boolean(initial) });
  const setValue = useCallback((next: SetStateAction<T>) => {
    setState((current) => {
      const value = typeof next === "function" ? (next as (previous: T) => T)(current.value) : next;
      return { value, retained: value ? value : current.retained, open: Boolean(value) };
    });
  }, []);
  const onOpenChangeComplete = useCallback(
    (open: boolean) => {
      if (!open)
        setState((current) => (current.open ? current : { ...current, retained: initial }));
    },
    [initial],
  );
  return [state.retained, setValue, state.open, onOpenChangeComplete] as const;
}

export function Slideover({
  open,
  onOpenChange,
  onOpenChangeComplete,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onOpenChangeComplete?: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange} onOpenChangeComplete={onOpenChangeComplete}>
      {children}
    </Sheet>
  );
}

export function SlideoverContent({
  children,
  size = "lg",
  width,
  className,
}: {
  children: ReactNode;
  size?: SlideoverSize;
  width?: CSSProperties["width"];
  className?: string;
}) {
  const contentRef = useRef<HTMLDivElement>(null);
  const panelWidth = typeof width === "number" ? `${width}px` : (width ?? sizeWidths[size]);
  return (
    <SheetContent
      ref={contentRef}
      side="right"
      showCloseButton={false}
      initialFocus={(interaction) =>
        interaction === "keyboard"
          ? (contentRef.current?.querySelector<HTMLElement>(
              'input:not(:disabled), textarea:not(:disabled), [role="combobox"]:not([aria-disabled="true"])',
            ) ?? true)
          : contentRef.current
      }
      style={
        {
          "--sheet-max-width": panelWidth,
        } as CSSProperties
      }
      className={cn("min-h-0 gap-0 overflow-hidden bg-card outline-none", className)}
    >
      {children}
    </SheetContent>
  );
}

export function SlideoverHeader({
  title,
  description,
  icon: Icon,
  iconClassName,
  children,
  className,
  showCloseButton = true,
}: {
  title: ReactNode;
  description?: ReactNode;
  icon?: LucideIcon;
  iconClassName?: string;
  children?: ReactNode;
  className?: string;
  showCloseButton?: boolean;
}) {
  return (
    <SheetHeader
      className={cn(
        "relative shrink-0 flex-row items-start justify-between gap-5 border-b border-border bg-card px-6 py-5 sm:px-8 sm:py-6",
        className,
      )}
    >
      <div className="flex min-w-0 items-start gap-3.5">
        {Icon && (
          <span
            className={cn(
              "grid size-10 shrink-0 place-items-center rounded-xl bg-primary/8 text-primary",
              iconClassName,
            )}
          >
            <Icon className="size-5" strokeWidth={1.75} />
          </span>
        )}
        <div className="min-w-0 space-y-2">
          <SheetTitle className="font-display text-xl font-semibold leading-tight tracking-tight">
            {title}
          </SheetTitle>
          {description && (
            <SheetDescription className="max-w-lg text-sm leading-relaxed text-muted-foreground">
              {description}
            </SheetDescription>
          )}
          {children}
        </div>
      </div>

      {showCloseButton && (
        <SheetClose
          render={<Button variant="ghost" size="icon" />}
          className="-mr-1 -mt-1 size-9 shrink-0 rounded-full text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          aria-label="Close panel"
        >
          <X />
        </SheetClose>
      )}
    </SheetHeader>
  );
}

export function SlideoverBody({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      data-slot="slideover-body"
      className={cn(
        "min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 py-6 sm:px-8",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function SlideoverFooter({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <SheetFooter
      className={cn(
        "mt-0 shrink-0 flex-row flex-wrap items-center justify-end gap-3 border-t border-border bg-muted/25 px-6 py-5 pb-[max(1.25rem,env(safe-area-inset-bottom))] sm:px-8 [&>[data-slot=button]]:min-w-24",
        className,
      )}
    >
      {children}
    </SheetFooter>
  );
}
