import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useMutation } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { errorToast } from "@/lib/toast";
import type { Account } from "@/types";
import { api, endpoints } from "@/api";
import { Logo } from "@/components/layout/Logo";
import { ThemeDropdown } from "@/components/layout/ThemeDropdown";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

const boardRows = [
  { name: "checkout", pattern: [31, 32, 33] },
  { name: "api", pattern: [] },
  { name: "marketing", pattern: [12] },
  { name: "auth", pattern: [] },
  { name: "webhooks", pattern: [40, 41] },
  { name: "nightly-backup", pattern: [] },
];

function HealthBoard() {
  return (
    <div aria-hidden="true" className="w-full max-w-md space-y-3">
      {boardRows.map((row, rowIndex) => (
        <div key={row.name} className="grid grid-cols-[7.5rem_minmax(0,1fr)] items-center gap-4">
          <span className="truncate font-mono text-2xs text-white/55">{row.name}</span>
          <span className="flex h-5 gap-[3px]">
            {Array.from({ length: 48 }, (_, cell) => {
              const down = row.pattern.includes(cell);
              const warn = !down && (cell * 7 + rowIndex * 13) % 53 === 0;
              return (
                <span
                  key={cell}
                  className={cn(
                    "flex-1 rounded-[2px]",
                    down ? "bg-[#f2666c]" : warn ? "bg-[#f5b740]" : "bg-[#3cc66f]/80",
                    cell === 47 && "motion-safe:animate-pulse",
                  )}
                />
              );
            })}
          </span>
        </div>
      ))}
      <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-4 pt-1 font-mono text-3xs text-white/35">
        <span />
        <span className="flex justify-between">
          <span>24h ago</span>
          <span>now</span>
        </span>
      </div>
    </div>
  );
}

function destinationAfterSignIn(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/app";
  return next;
}

export function Login() {
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [challenge, setChallenge] = useState("");
  const [notice, setNotice] = useState("");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const destination = destinationAfterSignIn(searchParams.get("next"));

  const requestMutation = useMutation({
    mutationFn: () =>
      api<{ challenge_id: string; expires_in: number }>(endpoints.auth.requestOtp, {
        method: "POST",
        body: JSON.stringify({ email: email.trim() }),
      }),
    onSuccess: (data) => {
      setChallenge(data.challenge_id);
      setCode("");
      setNotice(`We sent a 6-digit code to ${email.trim()}.`);
    },
    onError: (err: Error) => {
      errorToast(err, "The code could not be sent. Check the address and try again.");
    },
  });

  const verifyMutation = useMutation({
    mutationFn: () =>
      api<{ account: Account }>(endpoints.auth.verifyOtp, {
        method: "POST",
        body: JSON.stringify({
          challenge_id: challenge,
          email: email.trim(),
          code: code.trim(),
        }),
      }),
    onSuccess: () => {
      void navigate(destination);
    },
    onError: (err: Error) => {
      errorToast(err, "That code did not match. Check it, or send a new one.");
    },
  });

  const codeSent = Boolean(challenge);

  return (
    <main className="grid min-h-svh bg-background lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <section className="relative flex min-h-svh flex-col px-6 py-6 sm:px-12">
        <div className="flex items-center justify-between">
          <Logo />
          <ThemeDropdown />
        </div>

        <div className="mx-auto flex w-full max-w-sm flex-1 flex-col justify-center py-12">
          {!codeSent ? (
            <form
              className="space-y-6"
              onSubmit={(e) => {
                e.preventDefault();
                requestMutation.mutate();
              }}
            >
              <div className="space-y-2">
                <h1 className="font-display text-3xl font-semibold">Sign in</h1>
                <p className="text-sm text-muted-foreground">
                  We will email you a 6-digit code. There is no password to remember or leak.
                </p>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="email-address">Work email</Label>
                <Input
                  id="email-address"
                  size="lg"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  autoComplete="email"
                  placeholder="you@company.com"
                  required
                />
              </div>
              <Button
                size="xl"
                type="submit"
                className="w-full"
                disabled={requestMutation.isPending || !email.trim()}
              >
                {requestMutation.isPending ? "Sending code…" : "Email me a code"}
                {!requestMutation.isPending && <ArrowRight data-icon="inline-end" />}
              </Button>
            </form>
          ) : (
            <form
              className="space-y-6 animate-in fade-in slide-in-from-bottom-1 duration-200"
              onSubmit={(e) => {
                e.preventDefault();
                verifyMutation.mutate();
              }}
            >
              <button
                type="button"
                onClick={() => {
                  setChallenge("");
                  setCode("");
                  setNotice("");
                }}
                className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 text-xs text-muted-foreground hover:text-foreground"
              >
                <ArrowLeft className="size-3.5" />
                Use a different email
              </button>
              <div className="space-y-2">
                <h1 className="font-display text-3xl font-semibold">Check your inbox</h1>
                <p className="text-sm text-muted-foreground" aria-live="polite">
                  {notice}
                </p>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="6-digit-passcode">6-digit code</Label>
                <Input
                  size="lg"
                  id="6-digit-passcode"
                  className="h-12 text-center font-mono text-xl tracking-[0.5em]"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  placeholder="000000"
                  required
                />
              </div>
              <Button
                size="xl"
                type="submit"
                className="w-full"
                disabled={verifyMutation.isPending || code.length < 6}
              >
                {verifyMutation.isPending ? "Signing in…" : "Sign in"}
              </Button>
              <p className="text-center text-xs text-muted-foreground">
                No email after a minute?{" "}
                <button
                  type="button"
                  onClick={() => requestMutation.mutate()}
                  disabled={requestMutation.isPending}
                  className="font-medium text-foreground underline-offset-4 hover:underline disabled:opacity-50"
                >
                  Send a new code
                </button>
              </p>
            </form>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          Building on the API?{" "}
          <a
            href="/docs"
            className="font-medium text-foreground underline-offset-4 hover:underline"
          >
            Read the docs
          </a>
        </p>
      </section>

      <section className="relative hidden overflow-hidden bg-[#0a0e13] p-12 text-white lg:flex lg:flex-col lg:justify-center">
        <div className="mx-auto w-full max-w-md space-y-10">
          <div className="space-y-3">
            <p className="flex items-center gap-2 font-mono text-2xs text-white/55">
              <span className="relative inline-flex size-2">
                <span className="absolute inset-0 rounded-full bg-[#f2666c]/70 motion-safe:animate-ping" />
                <span className="relative size-2 rounded-full bg-[#f2666c]" />
              </span>
              checkout · 503 from 2 regions · 3m ago
            </p>
            <h2 className="font-display text-4xl leading-[1.1] font-semibold">
              Know before your customers do.
            </h2>
            <p className="text-sm text-white/60">
              Checks from several regions, alerts on the channels your team already uses, and a
              status page that updates itself.
            </p>
          </div>
          <HealthBoard />
        </div>
      </section>
    </main>
  );
}
