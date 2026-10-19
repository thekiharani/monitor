import type { LucideIcon } from "lucide-react";
import {
  ArrowUpRight,
  BookOpen,
  CloudOff,
  Compass,
  Construction,
  LockKeyhole,
  SearchX,
  ServerCrash,
  ShieldX,
  TimerOff,
  TrafficCone,
  WifiOff,
} from "lucide-react";
import { Link, useLocation } from "react-router-dom";
import { Logo } from "@/components/layout/Logo";
import { Button } from "@/components/ui/button";
import { ThemeDropdown } from "@/components/layout/ThemeDropdown";
import { cn } from "@/lib/utils";
import { usePageMetadata } from "@/lib/metadata";

interface Preset {
  icon: LucideIcon;
  badge: string;
  title: string;
  message: string;
  code: string | number;
  tone: "client" | "server" | "neutral";
}

const PRESETS: Record<number, Preset> = {
  400: {
    icon: SearchX,
    code: 400,
    badge: "Bad request",
    title: "That request doesn't look right",
    message:
      "The server couldn't understand what you sent. Check the link and try again — a stray character is all it takes.",
    tone: "client",
  },
  401: {
    icon: LockKeyhole,
    code: 401,
    badge: "Unauthorized",
    title: "Sign in required",
    message:
      "You need a valid session to open this page. Head back and sign in — it's passwordless and takes a few seconds.",
    tone: "client",
  },
  403: {
    icon: ShieldX,
    code: 403,
    badge: "Forbidden",
    title: "You don't have access here",
    message:
      "This workspace is reserved for the account that owns it. If you believe this is a mistake, contact the workspace owner.",
    tone: "client",
  },
  404: {
    icon: Compass,
    code: 404,
    badge: "Page not found",
    title: "This page went off the map",
    message:
      "The page you're looking for doesn't exist, was moved, or never made it here. Let's get you back on track.",
    tone: "neutral",
  },
  405: {
    icon: TrafficCone,
    code: 405,
    badge: "Method not allowed",
    title: "That pathway is closed",
    message:
      "This resource doesn't accept that type of request. Use the link or action that points to it instead.",
    tone: "client",
  },
  408: {
    icon: TimerOff,
    code: 408,
    badge: "Request timed out",
    title: "That took a bit too long",
    message:
      "The server waited for your request and gave up. Refresh and try again, or check your connection.",
    tone: "client",
  },
  410: {
    icon: Compass,
    code: 410,
    badge: "Gone",
    title: "It's no longer available",
    message:
      "This page used to exist but has been permanently removed. Nothing is broken — it's just gone.",
    tone: "neutral",
  },
  429: {
    icon: TrafficCone,
    code: 429,
    badge: "Too many requests",
    title: "Slow down a little",
    message:
      "That's too many requests in a short window. Give it a moment and try again — you're probably not stuck in a loop.",
    tone: "client",
  },
  500: {
    icon: ServerCrash,
    code: 500,
    badge: "Server error",
    title: "Something broke on our end",
    message:
      "An unexpected error hit the server. It wasn't your fault — we've logged it and are on it. Try again in a moment.",
    tone: "server",
  },
  502: {
    icon: WifiOff,
    code: 502,
    badge: "Bad gateway",
    title: "Something upstream went down",
    message:
      "One of our upstream services didn't respond. We're already rerouting, so a refresh should get you through.",
    tone: "server",
  },
  503: {
    icon: CloudOff,
    code: 503,
    badge: "Service unavailable",
    title: "We're down for a quick breather",
    message:
      "This service is temporarily unavailable, usually for maintenance. We'll be back shortly — hang tight.",
    tone: "server",
  },
  504: {
    icon: TimerOff,
    code: 504,
    badge: "Gateway timeout",
    title: "The upstream didn't make it",
    message:
      "A downstream service took too long and the gateway gave up. A refresh usually resolves it once things settle.",
    tone: "server",
  },
};

function defaultPreset(code: number): Preset {
  const server = code >= 500;
  return {
    icon: server ? Construction : Compass,
    code,
    badge: server ? "Server error" : "Request error",
    title: server ? "Oops — our side hit a snag" : "This page isn't available",
    message: server
      ? "Something went wrong while handling your request. We've been notified and will get it sorted."
      : "We couldn't find or accept that request. Double-check the address and try again.",
    tone: server ? "server" : "client",
  };
}

const TIPS: Record<number, string> = {
  400: "Tip: a stray space or a & that should be %26 can turn a good URL into a bad one.",
  401: "Tip: your session may have expired. Signing in again is all it takes.",
  403: "Tip: signing in with the workspace that owns this page usually fixes this.",
  404: "Tip: check the address for typos — a single swapped letter sends you somewhere else.",
  405: "Tip: use the same link the app gives you instead of typing a URL by hand.",
  410: "Tip: the link may have expired. Ask the person who shared it for a fresh one.",
  429: "Tip: waiting a few seconds usually clears this. You're probably not stuck in a loop.",
  500: "Tip: reloading once often gets you through while we patch things up.",
  502: "Tip: a quick refresh usually lands you on a working path.",
  503: "Tip: hang tight and try again in a few minutes.",
  504: "Tip: give it ten seconds and refresh — things settle quickly.",
};

const QUICK_LINKS: { to: string; label: string }[] = [
  { to: "/app", label: "Overview" },
  { to: "/app/usage", label: "Usage" },
  { to: "/app/api-keys", label: "API keys" },
  { to: "/docs", label: "Documentation" },
];

const toneLight: Record<Preset["tone"], string> = {
  server: "bg-status-down",
  client: "bg-status-warn",
  neutral: "bg-status-idle",
};

function ErrorContent({ code }: { code: number }) {
  const preset = PRESETS[code] ?? defaultPreset(code);
  const Icon = preset.icon;
  const { pathname } = useLocation();
  const tip =
    TIPS[code] ??
    (code >= 500
      ? "Try again shortly. The error is logged and we are looking into it."
      : "If you typed the address, check every character and try again.");

  return (
    <div className="w-full max-w-lg animate-in fade-in slide-in-from-bottom-2 duration-300">
      <p className="flex items-center gap-2 font-mono text-xs text-muted-foreground">
        <span className={cn("size-2 rounded-full", toneLight[preset.tone])} aria-hidden="true" />
        {preset.code} · {preset.badge}
      </p>
      <h1 className="mt-4 flex items-start gap-3 font-display text-3xl font-semibold text-balance sm:text-4xl">
        {preset.title}
        <Icon
          className="mt-1.5 hidden size-6 shrink-0 text-muted-foreground sm:block"
          strokeWidth={1.75}
        />
      </h1>
      <p className="mt-3 text-pretty text-muted-foreground">{preset.message}</p>

      {pathname && pathname !== "/" && (
        <p className="mt-6 truncate rounded-lg bg-muted px-3 py-2 font-mono text-xs text-muted-foreground">
          {pathname}
        </p>
      )}
      <p className="mt-4 text-sm text-muted-foreground">{tip}</p>

      <div className="mt-8 flex flex-wrap items-center gap-2">
        <Button size="xl" render={<Link to="/app" />}>
          Back to dashboard
          <ArrowUpRight data-icon="inline-end" />
        </Button>
        <Button size="xl" variant="outline" render={<Link to="/docs" />}>
          <BookOpen />
          Read the docs
        </Button>
      </div>

      <nav className="mt-6 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {QUICK_LINKS.map((link) => (
          <Link key={link.to} to={link.to} className="hover:text-foreground">
            {link.label}
          </Link>
        ))}
      </nav>
    </div>
  );
}

interface ErrorPageProps {
  code?: number;
  embedded?: boolean;
  className?: string;
}

export function ErrorPage({ code = 404, embedded = false, className }: ErrorPageProps) {
  const preset = PRESETS[code] ?? defaultPreset(code);
  usePageMetadata({ title: `${preset.badge} · ${preset.code}`, description: preset.message });

  if (embedded) {
    return (
      <div className={cn("grid min-h-[60vh] place-items-center px-2 py-16", className)}>
        <ErrorContent code={code} />
      </div>
    );
  }

  return (
    <div className="flex min-h-svh flex-col bg-background text-foreground">
      <header className="flex items-center justify-between px-6 py-5 sm:px-10">
        <Logo />
        <ThemeDropdown />
      </header>
      <main className="flex flex-1 items-center justify-center px-6 py-12">
        <ErrorContent code={code} />
      </main>
    </div>
  );
}

export function NotFound() {
  return <ErrorPage code={404} />;
}
