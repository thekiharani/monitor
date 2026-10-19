import {
  CircleCheckIcon,
  InfoIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

import { useTheme } from "@/context/theme-context";

function Toaster({ ...props }: ToasterProps) {
  const { resolvedTheme } = useTheme();

  return (
    <Sonner
      theme={resolvedTheme}
      className="toaster group"
      position="top-right"
      closeButton
      icons={{
        success: <CircleCheckIcon className="size-4" />,
        info: <InfoIcon className="size-4" />,
        warning: <TriangleAlertIcon className="size-4" />,
        error: <OctagonXIcon className="size-4" />,
        loading: <Loader2Icon className="size-4 animate-spin" />,
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius-lg)",
          "--success-bg": "color-mix(in oklch, var(--success) 12%, var(--popover))",
          "--success-text": "var(--success)",
          "--success-border": "color-mix(in oklch, var(--success) 30%, transparent)",
          "--warning-bg": "color-mix(in oklch, var(--warning) 12%, var(--popover))",
          "--warning-text": "var(--warning)",
          "--warning-border": "color-mix(in oklch, var(--warning) 30%, transparent)",
          "--info-bg": "color-mix(in oklch, var(--info) 12%, var(--popover))",
          "--info-text": "var(--info)",
          "--info-border": "color-mix(in oklch, var(--info) 30%, transparent)",
          "--error-bg": "color-mix(in oklch, var(--destructive) 12%, var(--popover))",
          "--error-text": "var(--destructive)",
          "--error-border": "color-mix(in oklch, var(--destructive) 30%, transparent)",
        } as React.CSSProperties
      }
      toastOptions={{
        classNames: {
          toast: "cn-toast font-sans text-sm shadow-lg",
        },
      }}
      {...props}
    />
  );
}

export { Toaster };
