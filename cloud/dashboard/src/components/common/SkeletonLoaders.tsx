import { Skeleton } from "@/components/ui/skeleton";
import { Card, CardContent, CardHeader } from "@/components/ui/card";

export function OverviewSkeleton() {
  return (
    <div className="space-y-8 animate-in fade-in duration-300">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="space-y-2">
          <Skeleton className="h-4 w-24 rounded-md" />
          <Skeleton className="h-9 w-64 rounded-xl" />
          <Skeleton className="h-4 w-80 rounded-md" />
        </div>
        <Skeleton className="h-10 w-32 rounded-xl" />
      </div>

      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 md:grid-cols-3">
        {[1, 2, 3].map((i) => (
          <Card key={i} className="p-5">
            <div className="flex items-center justify-between">
              <Skeleton className="size-10 rounded-xl" />
              <Skeleton className="h-4 w-12 rounded-full" />
            </div>
            <div className="mt-4 space-y-2">
              <Skeleton className="h-3 w-20 rounded" />
              <Skeleton className="h-8 w-28 rounded-lg" />
              <Skeleton className="h-3 w-36 rounded" />
            </div>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader className="p-6 pb-4">
          <Skeleton className="h-5 w-40 rounded" />
        </CardHeader>
        <CardContent className="p-0">
          <TableSkeleton rows={5} />
        </CardContent>
      </Card>
    </div>
  );
}

export function TableSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="divide-y divide-border">
      <div className="flex items-center justify-between px-6 py-3.5 bg-muted/30">
        <Skeleton className="h-4 w-24 rounded" />
        <Skeleton className="h-4 w-32 rounded" />
        <Skeleton className="h-4 w-16 rounded" />
        <Skeleton className="h-4 w-20 rounded" />
        <Skeleton className="h-4 w-8 rounded" />
      </div>
      {Array.from({ length: rows }, (_, i) => `row-${i}`).map((rowKey) => (
        <div key={rowKey} className="flex items-center justify-between px-6 py-4">
          <div className="space-y-1.5 min-w-[180px]">
            <Skeleton className="h-4 w-36 rounded" />
            <Skeleton className="h-3 w-24 rounded" />
          </div>
          <Skeleton className="h-3 w-48 rounded" />
          <Skeleton className="h-5 w-16 rounded-full" />
          <Skeleton className="h-3 w-20 rounded" />
          <Skeleton className="size-8 rounded-lg" />
        </div>
      ))}
    </div>
  );
}

export function UsageSkeleton() {
  return (
    <div className="space-y-8 animate-in fade-in duration-300">
      <div className="space-y-2">
        <Skeleton className="h-4 w-28 rounded-md" />
        <Skeleton className="h-9 w-48 rounded-xl" />
        <Skeleton className="h-4 w-72 rounded-md" />
      </div>

      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-3">
        {[1, 2, 3].map((i) => (
          <Card key={i} className="p-6">
            <div className="flex items-center justify-between">
              <Skeleton className="size-10 rounded-xl" />
              <Skeleton className="h-4 w-10 rounded" />
            </div>
            <div className="mt-6 space-y-2">
              <Skeleton className="h-3 w-24 rounded" />
              <Skeleton className="h-7 w-32 rounded-lg" />
            </div>
            <Skeleton className="mt-4 h-2 w-full rounded-full" />
          </Card>
        ))}
      </div>
    </div>
  );
}

export function PageSkeleton() {
  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      <div className="space-y-2">
        <Skeleton className="h-4 w-24 rounded-md" />
        <Skeleton className="h-9 w-60 rounded-xl" />
        <Skeleton className="h-4 w-96 rounded-md" />
      </div>
      <Card className="p-6">
        <div className="space-y-4">
          <Skeleton className="h-6 w-48 rounded" />
          <Skeleton className="h-24 w-full rounded-xl" />
          <Skeleton className="h-10 w-32 rounded-xl" />
        </div>
      </Card>
    </div>
  );
}
