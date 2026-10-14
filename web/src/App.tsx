import { lazy, Suspense, type ComponentType } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "@/components/layout/AppShell";
import { LoadingState, ErrorState } from "@/components/common/States";
import { Login } from "@/pages/Login";
import { Dashboard } from "@/pages/Dashboard";
import { NotFound } from "@/pages/NotFound";
import { SessionProvider } from "@/context/SessionContext";
import { useAuthConfig, useMe } from "@/api/hooks";
import { ApiError } from "@/api/client";

const Agents = lazy(() =>
  import("@/pages/Agents").then((m) => ({ default: m.Agents })),
);
const AgentDetail = lazy(() =>
  import("@/pages/AgentDetail").then((m) => ({ default: m.AgentDetail })),
);
const Endpoints = lazy(() =>
  import("@/pages/Endpoints").then((m) => ({ default: m.Endpoints })),
);
const EndpointDetail = lazy(() =>
  import("@/pages/EndpointDetail").then((m) => ({ default: m.EndpointDetail })),
);
const Network = lazy(() =>
  import("@/pages/Network").then((m) => ({ default: m.Network })),
);
const Services = lazy(() =>
  import("@/pages/Services").then((m) => ({ default: m.Services })),
);
const ServiceDetail = lazy(() =>
  import("@/pages/ServiceDetail").then((m) => ({ default: m.ServiceDetail })),
);
const Cloudflare = lazy(() =>
  import("@/pages/Cloudflare").then((m) => ({ default: m.Cloudflare })),
);
const Connections = lazy(() =>
  import("@/pages/Connections").then((m) => ({ default: m.Connections })),
);
const ConnectionDetail = lazy(() =>
  import("@/pages/ConnectionDetail").then((m) => ({
    default: m.ConnectionDetail,
  })),
);
const Members = lazy(() =>
  import("@/pages/Members").then((m) => ({ default: m.Members })),
);
const Settings = lazy(() =>
  import("@/pages/Settings").then((m) => ({ default: m.Settings })),
);
const Audit = lazy(() =>
  import("@/pages/Audit").then((m) => ({ default: m.Audit })),
);

function Page({ component: Component }: { component: ComponentType }) {
  return (
    <Suspense fallback={<LoadingState label="Loading" />}>
      <Component />
    </Suspense>
  );
}

function AppRoutes() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Dashboard />} />
          <Route path="agents" element={<Page component={Agents} />} />
          <Route path="agents/:id" element={<Page component={AgentDetail} />} />
          <Route path="endpoints" element={<Page component={Endpoints} />} />
          <Route
            path="endpoints/:id"
            element={<Page component={EndpointDetail} />}
          />
          <Route path="services" element={<Page component={Services} />} />
          <Route
            path="services/:id"
            element={<Page component={ServiceDetail} />}
          />
          <Route path="cloudflare" element={<Page component={Cloudflare} />} />
          <Route
            path="connections"
            element={<Page component={Connections} />}
          />
          <Route
            path="connections/:id"
            element={<Page component={ConnectionDetail} />}
          />
          <Route path="network" element={<Page component={Network} />} />
          <Route path="topology" element={<Navigate to="/network" replace />} />
          <Route path="members" element={<Page component={Members} />} />
          <Route path="settings" element={<Page component={Settings} />} />
          <Route path="audit" element={<Page component={Audit} />} />
          <Route path="*" element={<NotFound />} />
        </Route>
        <Route path="/login" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}

export function App() {
  const authConfig = useAuthConfig();
  const authEnabled = authConfig.data?.auth_enabled ?? false;
  const me = useMe(authEnabled);

  if (authConfig.isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <LoadingState label="Starting mishmesh" />
      </div>
    );
  }

  if (authConfig.isError || !authConfig.data) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorState
          error={
            authConfig.error ?? new Error("Could not reach the control plane.")
          }
        />
      </div>
    );
  }

  const cfg = authConfig.data;

  if (cfg.auth_enabled) {
    if (me.isLoading) {
      return (
        <div className="flex h-full items-center justify-center">
          <LoadingState label="Checking session" />
        </div>
      );
    }
    const unauthenticated =
      me.isError && me.error instanceof ApiError && me.error.status === 401;
    if (unauthenticated || !me.data) {
      return <Login authConfig={cfg} />;
    }
  }

  return (
    <SessionProvider authConfig={cfg} me={me.data ?? null}>
      <AppRoutes />
    </SessionProvider>
  );
}
