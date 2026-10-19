import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ArrowDownRight,
  ArrowUpRight,
  CalendarClock,
  Check,
  CreditCard,
  ShieldAlert,
  Smartphone,
  Sparkles,
} from "lucide-react";
import { toast } from "sonner";
import { api, endpoints, queryKeys } from "@/api";
import type { PublicPlan } from "@/types";
import { ActionDialog } from "@/components/common/ActionDialog";
import { useSlideoverState } from "@/components/ui/slideover";
import { errorToast } from "@/lib/toast";
import { useTimezone } from "@/context/timezone-context";
import { PageHeader } from "@/components/layout/Header";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { currencySymbol, formatMinor, formatUSD } from "@/lib/pricing";
import { cn } from "@/lib/utils";

type PaidPlan = "starter" | "pro" | "business";

type PaystackTransaction = {
  reference?: string;
  status?: string;
};

type PaystackCallbacks = {
  onSuccess?: (transaction: PaystackTransaction) => void;
  onCancel?: () => void;
  onError?: (error: { message?: string }) => void;
};

type PaystackPopConstructor = new () => {
  resumeTransaction: (accessCode: string, callbacks?: PaystackCallbacks) => void;
};

declare global {
  interface Window {
    PaystackPop?: PaystackPopConstructor;
  }
}

let paystackScriptPromise: Promise<PaystackPopConstructor> | undefined;

function loadPaystack(): Promise<PaystackPopConstructor> {
  if (window.PaystackPop) return Promise.resolve(window.PaystackPop);
  if (paystackScriptPromise) return paystackScriptPromise;

  const scriptPromise = new Promise<PaystackPopConstructor>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = "https://js.paystack.co/v2/inline.js";
    script.async = true;
    script.dataset.noriaPaystack = "inline";
    script.addEventListener(
      "load",
      () => {
        if (window.PaystackPop) resolve(window.PaystackPop);
        else reject(new Error("Paystack InlineJS did not initialize"));
      },
      { once: true },
    );
    script.addEventListener("error", () => reject(new Error("Paystack InlineJS failed to load")), {
      once: true,
    });
    document.head.appendChild(script);
  });
  paystackScriptPromise = scriptPromise.catch((error): never => {
    paystackScriptPromise = undefined;
    throw error;
  });

  return paystackScriptPromise;
}

type BillingAction = {
  data: {
    authorization_url?: string;
    access_code?: string;
    reference?: string;
    management_url?: string;
    status?: string;
    plan?: PaidPlan;
    effective_at?: string | null;
    amount_minor?: number;
    currency?: "KES" | "USD";
    credit_minor?: number;
    renewal_at?: string | null;
  };
};

type BillingSubscription = {
  amount_minor?: number;
  currency: "KES" | "USD";
  status: string;
  current_period_end: string | null;
  billing_mode?: "auto" | "manual" | "comp";
  scheduled_plan?: PaidPlan | null;
  scheduled_plan_effective_at?: string | null;
};

const paidPlans: Array<{ id: PaidPlan; name: string; description: string }> = [
  { id: "starter", name: "Starter", description: "For side projects and small services." },
  { id: "pro", name: "Pro", description: "For production apps that cannot go down quietly." },
  {
    id: "business",
    name: "Business",
    description: "For teams running many services across regions.",
  },
];

function planFeatures(plan: PublicPlan) {
  const interval = plan.min_interval_seconds;
  const frequency = interval % 60 === 0 ? `${interval / 60}-minute` : `${interval}-second`;
  return [
    `${plan.monitors} monitors`,
    `${frequency} checks from ${plan.regions} ${plan.regions === 1 ? "region" : "regions"}`,
    `${plan.status_pages} status ${plan.status_pages === 1 ? "page" : "pages"}`,
    ...(plan.custom_domains > 0
      ? [`${plan.custom_domains} custom ${plan.custom_domains === 1 ? "domain" : "domains"}`]
      : []),
    `${plan.alert_channels} alert channels`,
    `${plan.retention_days}-day history`,
  ];
}

export function Billing() {
  const timezone = useTimezone();
  const formatDate = (value?: string | null) => {
    if (!value) return "the end of the current billing period";
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeZone: timezone || undefined,
    }).format(new Date(value));
  };
  const queryClient = useQueryClient();
  const managementWindowRef = useRef<Window | null>(null);
  const [searchParams] = useSearchParams();
  const renewTriggeredRef = useRef(false);
  const [confirmation, setConfirmation, confirmationOpen, confirmationComplete] = useSlideoverState<
    "cancel" | "resume" | "manage" | { type: "downgrade"; plan: PaidPlan } | null
  >(null);
  const [selectedCurrency, setSelectedCurrency] = useState<"KES" | "USD">("KES");
  const [method, setMethod] = useState<"card" | "mpesa">("card");
  const billingQuery = useQuery({
    queryKey: queryKeys.billing,
    queryFn: () =>
      api<{ data: { plan: PaidPlan | "free"; subscription: BillingSubscription | null } }>(
        endpoints.billing,
      ),
  });
  const plansQuery = useQuery({
    queryKey: queryKeys.plans,
    queryFn: () => api<{ data: PublicPlan[] }>(endpoints.plans()),
  });
  const currency = billingQuery.data?.data.subscription?.currency ?? selectedCurrency;
  const actionMutation = useMutation({
    mutationFn: (path: string) => api<BillingAction>(path, { method: "POST" }),
    onSuccess: async (data) => {
      if (data.data.access_code) {
        await openCheckout(data);
        return;
      }
      if (data.data.status === "scheduled") {
        void queryClient.invalidateQueries({ queryKey: queryKeys.billing });
        toast.success(`Your downgrade to ${data.data.plan} is scheduled.`);
        return;
      }
      if (data.data.status === "active") {
        void queryClient.invalidateQueries({ queryKey: queryKeys.billing });
        toast.success("Your subscription has been resumed.");
        return;
      }
      if (data.data.status === "resume_scheduled") {
        void queryClient.invalidateQueries({ queryKey: queryKeys.billing });
        toast.success(`Your subscription will resume on ${formatDate(data.data.effective_at)}.`);
        return;
      }
      if (data.data.status === "non_renewing") {
        void queryClient.invalidateQueries({ queryKey: queryKeys.billing });
        toast.success("Your subscription is cancelled at the end of this billing period.");
        return;
      }
      const target = data.data.management_url;
      if (target) {
        const managementWindow = managementWindowRef.current;
        managementWindowRef.current = null;
        if (managementWindow && !managementWindow.closed) {
          managementWindow.location.replace(target);
          toast.success("Paystack payment management opened in a new tab.");
        } else {
          errorToast(
            undefined,
            "Your browser blocked the Paystack window. Allow popups for this site and try again.",
          );
        }
      } else {
        errorToast(undefined, "We couldn't open Paystack payment management. Please try again.");
      }
    },
    onError: (err: Error) => {
      managementWindowRef.current?.close();
      managementWindowRef.current = null;
      errorToast(
        err,
        "We couldn't complete that billing action. Your current plan is unchanged. Please try again.",
      );
    },
    onSettled: () => setConfirmation(null),
  });

  const renewMutation = useMutation({
    mutationFn: (plan: PaidPlan) =>
      api<BillingAction>(`/api/dashboard/v1/billing/renew?plan=${plan}&currency=${currency}`, {
        method: "POST",
      }),
    onSuccess: async (data) => {
      if (data.data.access_code) {
        await openCheckout(data);
        return;
      }
      if (data.data.authorization_url) window.location.assign(data.data.authorization_url);
      else errorToast(undefined, "We couldn't open Paystack checkout. Please try again.");
    },
    onError: (err: Error) =>
      errorToast(err, "We couldn't start your renewal. Your plan is unchanged. Please try again."),
  });

  const renewPlan = searchParams.get("renew") as PaidPlan | null;
  useEffect(() => {
    if (!renewPlan || renewTriggeredRef.current || !billingQuery.data) return;
    if (renewPlan === "starter" || renewPlan === "pro" || renewPlan === "business") {
      renewTriggeredRef.current = true;
      renewMutation.mutate(renewPlan);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [renewPlan, billingQuery.data]);

  const verifyInlinePayment = (reference: string) => {
    void api<{ data: { status: string } }>(
      `/api/dashboard/v1/billing/verify?reference=${encodeURIComponent(reference)}`,
      { method: "POST" },
    )
      .then(async () => {
        await queryClient.invalidateQueries({ queryKey: queryKeys.billing });
        toast.success("Payment received. Your plan is now active.");
        return true;
      })
      .catch(async () => {
        await billingQuery.refetch();
        toast.info("Payment received. Your plan is being updated.");
      });
  };

  const openCheckout = async (checkout: BillingAction) => {
    const fallback = checkout.data.authorization_url;
    if ((checkout.data.credit_minor ?? 0) > 0 && checkout.data.amount_minor) {
      toast.info(
        `${formatMinor(checkout.data.currency ?? currency, checkout.data.credit_minor ?? 0)} unused-plan credit applied. Pay ${formatMinor(checkout.data.currency ?? currency, checkout.data.amount_minor)} now; your billing cycle restarts today.`,
      );
    }
    if (!checkout.data.access_code) {
      if (fallback) window.location.assign(fallback);
      else errorToast(undefined, "We couldn't open Paystack checkout. Please try again.");
      return;
    }

    let fallbackUsed = false;
    const redirectToHostedCheckout = () => {
      if (fallbackUsed) return;
      fallbackUsed = true;
      if (fallback) window.location.assign(fallback);
      else errorToast(undefined, "We couldn't open Paystack checkout. Please try again.");
    };

    try {
      const PaystackPop = await loadPaystack();
      const popup = new PaystackPop();
      popup.resumeTransaction(checkout.data.access_code, {
        onSuccess: () => {
          const reference = checkout.data.reference;
          if (reference) verifyInlinePayment(reference);
          else {
            void billingQuery.refetch();
            toast.info("Payment received. Your plan is being updated.");
          }
        },
        onCancel: () => undefined,
        onError: () => {
          errorToast(
            undefined,
            "Paystack checkout could not open. We'll try the secure hosted checkout instead.",
          );
          redirectToHostedCheckout();
        },
      });
    } catch {
      errorToast(
        undefined,
        "Paystack checkout could not open. We'll try the secure hosted checkout instead.",
      );
      redirectToHostedCheckout();
    }
  };

  if (billingQuery.isLoading || plansQuery.isLoading) return <PageSkeleton />;
  const billing = billingQuery.data?.data;
  const plans = plansQuery.data?.data;
  if (!billing || !plans || billingQuery.isError || plansQuery.isError) {
    return (
      <Alert variant="warning">
        <AlertTitle>Billing is temporarily unavailable</AlertTitle>
        <AlertDescription>
          We couldn't load your subscription and current prices. Please try again.
        </AlertDescription>
        <Button
          variant="outline"
          className="mt-3 w-fit"
          onClick={() => {
            void billingQuery.refetch();
            void plansQuery.refetch();
          }}
        >
          Try again
        </Button>
      </Alert>
    );
  }
  const freePlan = plans.find((plan) => plan.plan === "free");
  const isPaid = billing.plan !== "free";
  const currentPlan = billing.plan === "free" ? null : billing.plan;
  const scheduledPlan = billing.subscription?.scheduled_plan ?? null;
  const resumeScheduled = scheduledPlan === billing.plan;
  const manual = billing.subscription?.billing_mode === "manual";
  const complimentary = billing.subscription?.billing_mode === "comp";
  const periodEnd = billing.subscription?.current_period_end
    ? new Date(billing.subscription.current_period_end)
    : null;
  const renewalDue =
    manual &&
    billing.subscription?.status !== "non_renewing" &&
    periodEnd !== null &&
    (billing.subscription?.status === "past_due" ||
      periodEnd.getTime() - Date.now() < 4 * 24 * 60 * 60 * 1000);
  const checkout = (plan: PaidPlan) =>
    actionMutation.mutate(
      `/api/dashboard/v1/billing/checkout?plan=${plan}&method=${currency === "USD" ? "card" : method}&currency=${currency}`,
    );
  const changePlan = (plan: PaidPlan) => {
    if (complimentary) return;
    if (!currentPlan) return checkout(plan);
    if (planRank(plan) > planRank(currentPlan)) {
      checkout(plan);
    } else if (planRank(plan) < planRank(currentPlan)) {
      setConfirmation({ type: "downgrade", plan });
    }
  };
  const confirmAction = () => {
    if (confirmation === "cancel") {
      actionMutation.mutate("/api/dashboard/v1/billing/cancel");
    } else if (confirmation === "resume") {
      actionMutation.mutate("/api/dashboard/v1/billing/resume");
    } else if (confirmation === "manage") {
      const managementWindow = window.open("about:blank", "_blank");
      if (managementWindow) {
        managementWindow.opener = null;
        managementWindowRef.current = managementWindow;
      }
      actionMutation.mutate("/api/dashboard/v1/billing/manage-payment");
    } else if (confirmation) {
      actionMutation.mutate(`/api/dashboard/v1/billing/change-plan?plan=${confirmation.plan}`);
    }
  };

  return (
    <div className="space-y-8">
      <PageHeader
        title="Billing"
        description="Your plan, invoices and how you pay."
        breadcrumbs={[{ label: "Billing" }]}
      />
      <Card className="overflow-hidden">
        <div
          className={cn(
            "grid gap-6 p-5 sm:p-8 lg:grid-cols-[1fr_320px] lg:items-center",
            isPaid ? "bg-brand-soft/40" : "bg-card",
          )}
        >
          <div className="space-y-3">
            <Badge
              variant="outline"
              className={cn(
                "rounded-full px-3 py-1 font-semibold text-xs",
                isPaid
                  ? "border-primary/40 bg-primary/10 text-primary"
                  : "border-border bg-card text-muted-foreground",
              )}
            >
              {complimentary
                ? `${billing.plan} Plan Complimentary`
                : isPaid
                  ? `${billing.plan} Plan Active`
                  : "Free Plan"}
            </Badge>
            <h2 className="text-2xl font-semibold tracking-tight text-foreground sm:text-3xl">
              {isPaid
                ? `You are on the ${billing.plan} Plan`
                : "Choose a plan that fits your usage"}
            </h2>
            <p className="max-w-lg text-xs leading-relaxed text-muted-foreground sm:text-sm">
              {complimentary
                ? billing.subscription?.current_period_end
                  ? `Noria Monitor provides this plan at no charge until ${formatDate(billing.subscription.current_period_end)}. There is nothing to pay.`
                  : "Noria Monitor provides this plan at no charge. There is nothing to pay, and it does not expire."
                : resumeScheduled
                  ? `Your subscription is scheduled to resume on ${formatDate(billing.subscription?.scheduled_plan_effective_at)}.`
                  : scheduledPlan
                    ? `Your plan will change to ${scheduledPlan} on ${formatDate(billing.subscription?.scheduled_plan_effective_at)}.`
                    : isPaid
                      ? billing.subscription?.status === "non_renewing"
                        ? "Your subscription is set to end at the conclusion of this billing period."
                        : "Your plan is active with the limits shown below."
                      : "Start small and upgrade when you need more monitors, faster checks or more regions."}
            </p>
          </div>
          <div className="flex flex-col gap-2.5 sm:flex-row lg:flex-col lg:items-end">
            {complimentary ? (
              <Badge
                variant="outline"
                className="self-start rounded-full border-primary/40 bg-primary/10 px-3 py-1 font-semibold text-xs text-primary"
              >
                <Sparkles className="mr-1.5 size-3.5" />
                No charge
              </Badge>
            ) : isPaid ? (
              <>
                {manual ? (
                  <Badge
                    variant="outline"
                    className="self-start rounded-full border-primary/40 bg-primary/10 px-3 py-1 font-semibold text-xs text-primary"
                  >
                    <Smartphone className="mr-1.5 size-3.5" />
                    {currency === "KES" ? "Pay via M-Pesa" : "Pay by card"}
                  </Badge>
                ) : (
                  <Button
                    className="rounded-lg bg-primary px-4 font-semibold text-primary-foreground shadow-sm hover:bg-primary/90 sm:px-5"
                    onClick={() => setConfirmation("manage")}
                    disabled={actionMutation.isPending}
                  >
                    <CreditCard className="mr-2 size-4 shrink-0" />
                    <span className="truncate">Manage Payment &amp; Invoices</span>
                  </Button>
                )}
                {renewalDue ? (
                  <Button
                    className="rounded-lg bg-primary px-5 font-semibold text-primary-foreground shadow-sm hover:bg-primary/90"
                    onClick={() => renewMutation.mutate(currentPlan ?? "pro")}
                    disabled={renewMutation.isPending}
                  >
                    <ShieldAlert className="mr-2 size-4" />
                    {renewMutation.isPending
                      ? "Connecting…"
                      : billing.subscription?.status === "past_due"
                        ? "Pay now to keep your plan"
                        : "Pay next month now"}
                  </Button>
                ) : null}
                {billing.subscription?.status === "non_renewing" ? (
                  resumeScheduled ? (
                    <Button variant="outline" className="rounded-lg text-xs" disabled>
                      <CalendarClock className="mr-2 size-4" />
                      Resume Scheduled
                    </Button>
                  ) : (
                    <Button
                      variant="outline"
                      className="rounded-lg text-xs"
                      onClick={() => setConfirmation("resume")}
                      disabled={actionMutation.isPending}
                    >
                      <ArrowUpRight className="mr-2 size-4" />
                      Resume Subscription
                    </Button>
                  )
                ) : (
                  <Button
                    variant="ghost"
                    className="rounded-lg text-xs text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    onClick={() => setConfirmation("cancel")}
                    disabled={actionMutation.isPending}
                  >
                    Cancel Subscription
                  </Button>
                )}
              </>
            ) : (
              <Button
                size="xl"
                onClick={() => checkout("starter")}
                disabled={actionMutation.isPending}
              >
                <Sparkles className="mr-2 size-4" />
                {actionMutation.isPending ? "Connecting…" : "Start with Starter"}
                <ArrowUpRight className="ml-1.5 size-4" />
              </Button>
            )}
          </div>
        </div>
      </Card>
      {renewalDue && periodEnd && (
        <Card className="bg-warning/10 ring-warning/40">
          <div className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <p className="text-sm font-semibold text-foreground">
                {billing.subscription?.status === "past_due"
                  ? "Your plan is past due"
                  : "Your plan renews soon"}
              </p>
              <p className="text-xs text-muted-foreground">
                {billing.subscription?.status === "past_due"
                  ? `Pay the ${billing.plan} monthly fee now to keep your plan active. Your billing period ended ${formatDate(billing.subscription?.current_period_end)}.`
                  : `Your ${billing.plan} period ends ${formatDate(billing.subscription?.current_period_end)}. We emailed you a pay link; you can also pay now with M-Pesa or a card.`}
              </p>
            </div>
            <Button
              className="shrink-0 rounded-lg bg-primary font-semibold text-primary-foreground"
              onClick={() => renewMutation.mutate(currentPlan ?? "pro")}
              disabled={renewMutation.isPending}
            >
              <Smartphone className="mr-2 size-4" />
              {renewMutation.isPending ? "Connecting…" : "Pay now"}
            </Button>
          </div>
        </Card>
      )}
      {!isPaid && !complimentary && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">Billing currency</legend>
          <div
            className="inline-flex rounded-xl border border-border bg-card p-1"
            role="group"
            aria-label="Billing currency"
          >
            {(["KES", "USD"] as const).map((option) => (
              <button
                key={option}
                type="button"
                aria-pressed={currency === option}
                onClick={() => {
                  setSelectedCurrency(option);
                  if (option === "USD") setMethod("card");
                }}
                className={cn(
                  "rounded-lg px-4 py-2 text-xs font-medium",
                  currency === option
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >{`${option === "KES" ? "Kenyan shilling" : "US dollar"} (${currencySymbol(option)})`}</button>
            ))}
          </div>
          <p className="text-xs text-muted-foreground">
            {currency === "KES"
              ? "Pay in Kenyan shillings with M-Pesa or a card."
              : "Pay in US dollars with a card."}
          </p>
        </fieldset>
      )}
      {isPaid && !complimentary && (
        <p className="text-xs text-muted-foreground">
          Your subscription is billed in{" "}
          {`${currency === "KES" ? "Kenyan shillings" : "US dollars"} (${currencySymbol(currency)})`}
          .
          {billing.subscription?.amount_minor !== undefined && (
            <> Your monthly price is {formatMinor(currency, billing.subscription.amount_minor)}.</>
          )}{" "}
          Renewals and plan changes use this currency.
        </p>
      )}
      {!isPaid && !complimentary && currency === "KES" && (
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-xs font-medium text-muted-foreground">
            Choose how you want to pay each month.
          </p>
          <div className="inline-flex rounded-xl border border-border bg-card p-1 shadow-none">
            <button
              type="button"
              onClick={() => setMethod("mpesa")}
              className={cn(
                "flex items-center gap-2 rounded-lg px-4 py-2 text-xs font-medium",
                method === "mpesa"
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Smartphone className="size-4" />
              M-Pesa
            </button>
            <button
              type="button"
              onClick={() => setMethod("card")}
              className={cn(
                "flex items-center gap-2 rounded-lg px-4 py-2 text-xs font-medium",
                method === "card"
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              <CreditCard className="size-4" />
              Card
            </button>
          </div>
        </div>
      )}
      <div className="grid grid-cols-[minmax(0,1fr)] gap-6 md:grid-cols-2 xl:grid-cols-4">
        {freePlan && (
          <PlanCard
            name="Free"
            description="For trying Noria Monitor on a few services."
            price={formatMinor(currency, 0)}
            secondaryPrice="No charge"
            active={!isPaid}
            features={planFeatures(freePlan)}
          />
        )}
        {paidPlans.map((plan) => {
          const pricing = plans.find((item) => item.plan === plan.id);
          if (!pricing) return null;
          return (
            <PlanCard
              key={plan.id}
              name={plan.name}
              description={plan.description}
              price={
                currency === "KES"
                  ? formatMinor("KES", pricing.price_minor)
                  : formatUSD(pricing.price_minor)
              }
              secondaryPrice={`${currency === "KES" ? formatUSD(pricing.price_minor) : formatMinor("KES", pricing.price_minor)} / month`}
              active={billing.plan === plan.id}
              features={planFeatures(pricing)}
              actionLabel={
                !isPaid || !currentPlan
                  ? `Upgrade to ${plan.name}`
                  : planRank(plan.id) > planRank(currentPlan)
                    ? `Upgrade to ${plan.name}`
                    : `Downgrade to ${plan.name}`
              }
              onChange={
                !complimentary && billing.plan !== plan.id ? () => changePlan(plan.id) : undefined
              }
            />
          );
        })}
      </div>
      <ActionDialog
        open={confirmationOpen}
        onOpenChangeComplete={confirmationComplete}
        onOpenChange={(open) => !open && setConfirmation(null)}
        title={
          confirmation === "cancel"
            ? "Cancel your subscription?"
            : confirmation === "resume"
              ? "Resume your subscription?"
              : confirmation === "manage"
                ? "Manage payment details?"
                : `Schedule downgrade to ${confirmation?.plan ?? ""}?`
        }
        description={
          confirmation === "cancel"
            ? `You will keep your ${billing.plan} plan until ${formatDate(billing.subscription?.current_period_end)}. It will not renew after that date.`
            : confirmation === "resume"
              ? "Your subscription will renew normally at the next billing date."
              : confirmation === "manage"
                ? "Paystack will open in a new tab where you can update your payment method and review subscription billing options."
                : `Your current ${billing.plan} plan will remain active until ${formatDate(billing.subscription?.current_period_end)}. The ${confirmation?.plan ?? "selected"} plan will begin after that period.`
        }
        icon={
          confirmation === "cancel"
            ? undefined
            : confirmation === "manage"
              ? CreditCard
              : CalendarClock
        }
        tone={
          confirmation === "cancel" ? "danger" : confirmation === "manage" ? "primary" : "warning"
        }
        confirmLabel={
          confirmation === "cancel"
            ? "Cancel subscription"
            : confirmation === "resume"
              ? "Resume subscription"
              : confirmation === "manage"
                ? "Open Paystack"
                : "Schedule downgrade"
        }
        isLoading={actionMutation.isPending}
        onConfirm={confirmAction}
      />
    </div>
  );
}

function PlanCard({
  name,
  description,
  price,
  secondaryPrice,
  active,
  features,
  actionLabel,
  onChange,
}: {
  name: string;
  description: string;
  price: string;
  secondaryPrice: string;
  active: boolean;
  features: string[];
  actionLabel?: string;
  onChange?: () => void;
}) {
  return (
    <Card className={cn("p-6", active && "ring-2 ring-primary")}>
      <div className="flex items-center justify-between">
        <h3 className="flex items-center gap-1.5 text-lg font-semibold text-foreground">
          {name !== "Free" && <Sparkles className="size-4 text-warning" />}
          {name}
        </h3>
        <Badge
          variant={active ? "default" : "outline"}
          className={cn(
            "text-xs",
            active && "bg-primary text-primary-foreground",
            !active && "border-primary/40 text-primary",
          )}
        >
          {active ? "Active" : "Available"}
        </Badge>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{description}</p>
      <div className="mt-4 border-t border-border pt-4">
        <p className="text-2xl font-semibold text-foreground">
          {price} <span className="text-xs font-normal text-muted-foreground">/ month</span>
        </p>
        {secondaryPrice && <p className="mt-1 text-xs text-muted-foreground">{secondaryPrice}</p>}
      </div>
      <ul className="mt-5 space-y-2.5 text-xs text-foreground font-medium">
        {features.map((feature) => (
          <li key={feature} className="flex items-center gap-2">
            <Check className="size-4 shrink-0 text-primary" />
            <span>{feature}</span>
          </li>
        ))}
      </ul>
      {onChange && (
        <Button
          className="mt-6 w-full rounded-lg bg-primary text-xs font-medium text-primary-foreground shadow-xs hover:bg-primary/90"
          onClick={onChange}
        >
          {actionLabel ?? `Change to ${name}`}
          {actionLabel?.startsWith("Downgrade") ? (
            <ArrowDownRight className="ml-1.5 size-4" />
          ) : (
            <ArrowUpRight className="ml-1.5 size-4" />
          )}
        </Button>
      )}
    </Card>
  );
}

function planRank(plan: PaidPlan) {
  return { starter: 1, pro: 2, business: 3 }[plan];
}
