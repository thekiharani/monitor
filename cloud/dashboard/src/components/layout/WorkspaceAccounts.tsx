import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { toast } from "sonner";
import type { WorkspaceAccount } from "@/types";
import { api, endpoints, queryKeys } from "@/api";
import { errorToast } from "@/lib/toast";
import { workspaceInitial, workspaceName } from "@/lib/identity";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";

export function useWorkspaceAccounts() {
  const queryClient = useQueryClient();

  const accountsQuery = useQuery({
    queryKey: queryKeys.accounts,
    queryFn: () => api<{ data: WorkspaceAccount[] }>(endpoints.accounts()),
  });

  const switchMutation = useMutation({
    mutationFn: (id: string) => api<void>(endpoints.switchAccount(id), { method: "POST" }),
    onSuccess: () => {
      queryClient.clear();
      toast.success("Switched workspace.");
    },
    onError: (error: Error) => errorToast(error, "We couldn't switch workspace."),
  });

  const accounts = accountsQuery.data?.data ?? [];

  return {
    accounts,
    current: accounts.find((account) => account.is_current),
    switchTo: switchMutation.mutate,
    switchingTo: switchMutation.isPending ? switchMutation.variables : undefined,
  };
}

export function WorkspaceAccountItems({
  accounts,
  switchTo,
  switchingTo,
}: {
  accounts: WorkspaceAccount[];
  switchTo: (id: string) => void;
  switchingTo?: string;
}) {
  return (
    <>
      {accounts.map((account) => (
        <DropdownMenuItem
          key={account.account_id}
          disabled={account.is_current || switchingTo !== undefined}
          onClick={() => switchTo(account.account_id)}
        >
          <Avatar size="sm">
            <AvatarFallback className="bg-brand-soft text-3xs font-bold text-primary">
              {workspaceInitial(account)}
            </AvatarFallback>
          </Avatar>
          <span className="min-w-0 flex-1 truncate">{workspaceName(account)}</span>
          {switchingTo === account.account_id ? (
            <Spinner className="size-3" />
          ) : (
            account.is_current && <Check className="size-3 text-primary" />
          )}
        </DropdownMenuItem>
      ))}
    </>
  );
}
