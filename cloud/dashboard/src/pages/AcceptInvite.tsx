import { useEffect, useRef } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, TriangleAlert } from "lucide-react";
import { toast } from "sonner";
import { api, endpoints, APIError } from "@/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { usePageMetadata } from "@/lib/metadata";

export function AcceptInvite() {
  const { token } = useParams<{ token: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const attempted = useRef(false);

  usePageMetadata({
    title: "Accept invitation",
    description: "Join a Noria Monitor workspace you were invited to.",
  });

  const accept = useMutation({
    mutationFn: () =>
      api<{ data: { account_id: string; role: string } }>(endpoints.acceptInvite(token ?? ""), {
        method: "POST",
      }),
    onSuccess: async (result) => {
      await api(endpoints.switchAccount(result.data.account_id), { method: "POST" });
      queryClient.clear();
      toast.success("You have joined the workspace.");
      void navigate("/app", { replace: true });
    },
  });

  useEffect(() => {
    if (!token || attempted.current) return;
    attempted.current = true;
    accept.mutate();
  }, [token, accept]);

  const failure = accept.error as APIError | undefined;

  return (
    <div className="mx-auto flex min-h-[60vh] max-w-md items-center px-4">
      <Card className="w-full">
        <CardContent className="flex flex-col items-center p-8 text-center">
          {accept.isPending || accept.isIdle ? (
            <>
              <span className="grid size-11 place-items-center rounded-lg bg-muted text-muted-foreground">
                <CircleCheck className="size-5" />
              </span>
              <h1 className="mt-4 font-display text-lg font-semibold text-foreground">
                Accepting your invitation…
              </h1>
              <p className="mt-1 text-xs text-muted-foreground">This only takes a moment.</p>
            </>
          ) : (
            <>
              <span className="grid size-11 place-items-center rounded-xl bg-destructive/10 text-destructive">
                <TriangleAlert className="size-5" />
              </span>
              <h1 className="mt-4 font-display text-lg font-semibold text-foreground">
                We couldn&apos;t accept this invitation
              </h1>
              <p className="mt-1 text-xs text-muted-foreground">
                {failure?.message ||
                  "The invitation may have expired, been revoked, or already been used."}
              </p>
              <Button
                size="xl"
                className="mt-5"
                onClick={() => navigate("/app", { replace: true })}
              >
                Go to your dashboard
              </Button>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
