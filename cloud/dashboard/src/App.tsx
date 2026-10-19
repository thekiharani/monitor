import { lazy, Suspense } from "react";
import { Route, Routes, useLocation, type Location } from "react-router-dom";
import { Shell } from "@/components/layout/Shell";
import { PageSkeleton } from "@/components/common/SkeletonLoaders";
import { ErrorPage } from "@/components/common/ErrorPage";
import { ErrorBoundary } from "@/components/common/ErrorBoundary";
import { RouteMetadata } from "@/lib/metadata";

const Control = lazy(() => import("@/pages/Control").then((m) => ({ default: m.Control })));
const Overview = lazy(() => import("@/pages/Overview").then((m) => ({ default: m.Overview })));
const Monitors = lazy(() => import("@/pages/Monitors").then((m) => ({ default: m.Monitors })));
const NewMonitor = lazy(() =>
  import("@/pages/MonitorForm").then((m) => ({ default: m.NewMonitor })),
);
const EditMonitor = lazy(() =>
  import("@/pages/MonitorForm").then((m) => ({ default: m.EditMonitor })),
);
const MonitorDetail = lazy(() =>
  import("@/pages/MonitorDetail").then((m) => ({ default: m.MonitorDetail })),
);
const Incidents = lazy(() => import("@/pages/Incidents").then((m) => ({ default: m.Incidents })));
const StatusPages = lazy(() =>
  import("@/pages/StatusPages").then((m) => ({ default: m.StatusPages })),
);
const StatusPageDetail = lazy(() =>
  import("@/pages/StatusPages").then((m) => ({ default: m.StatusPageDetail })),
);
const Alerts = lazy(() => import("@/pages/Alerts").then((m) => ({ default: m.Alerts })));
const APIKeys = lazy(() => import("@/pages/APIKeys").then((m) => ({ default: m.APIKeys })));
const Team = lazy(() => import("@/pages/Team").then((m) => ({ default: m.Team })));
const AcceptInvite = lazy(() =>
  import("@/pages/AcceptInvite").then((m) => ({ default: m.AcceptInvite })),
);
const Usage = lazy(() => import("@/pages/Usage").then((m) => ({ default: m.Usage })));
const Billing = lazy(() => import("@/pages/Billing").then((m) => ({ default: m.Billing })));
const Account = lazy(() => import("@/pages/Account").then((m) => ({ default: m.Account })));
const Login = lazy(() => import("@/pages/Login").then((m) => ({ default: m.Login })));

export function App() {
  const location = useLocation();
  const { pathname } = location;
  const backgroundLocation = (location.state as { backgroundLocation?: Location } | null)
    ?.backgroundLocation;
  return (
    <ErrorBoundary resetKey={pathname}>
      <Suspense
        fallback={
          <div className="mx-auto max-w-6xl p-8">
            <PageSkeleton />
          </div>
        }
      >
        <RouteMetadata />
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/control/*" element={<Control />} />
          <Route
            path="/app/*"
            element={
              <Shell>
                <ErrorBoundary resetKey={pathname} fallback={<ErrorPage code={500} embedded />}>
                  <Suspense
                    fallback={
                      <div className="space-y-6">
                        <PageSkeleton />
                      </div>
                    }
                  >
                    <Routes location={backgroundLocation ?? location}>
                      <Route index element={<Overview />} />
                      <Route path="monitors" element={<Monitors />} />
                      <Route path="monitors/new" element={<NewMonitor />} />
                      <Route path="monitors/:id" element={<MonitorDetail />} />
                      <Route path="monitors/:id/edit" element={<EditMonitor />} />
                      <Route path="incidents" element={<Incidents />} />
                      <Route path="alerts" element={<Alerts />} />
                      <Route path="status-pages" element={<StatusPages />} />
                      <Route path="status-pages/:id" element={<StatusPageDetail />} />
                      <Route path="team" element={<Team />} />
                      <Route path="invite/:token" element={<AcceptInvite />} />
                      <Route path="api-keys" element={<APIKeys />} />
                      <Route path="usage" element={<Usage />} />
                      <Route path="billing" element={<Billing />} />
                      <Route path="account" element={<Account />} />
                      <Route path="*" element={<ErrorPage code={404} embedded />} />
                    </Routes>
                    {backgroundLocation && (
                      <Routes>
                        <Route path="monitors/new" element={<NewMonitor />} />
                        <Route path="monitors/:id/edit" element={<EditMonitor />} />
                      </Routes>
                    )}
                  </Suspense>
                </ErrorBoundary>
              </Shell>
            }
          />
          <Route path="*" element={<ErrorPage code={404} />} />
        </Routes>
      </Suspense>
    </ErrorBoundary>
  );
}
