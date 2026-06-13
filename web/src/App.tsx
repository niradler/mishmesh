import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "@/components/layout/AppShell";
import { LoadingState, ErrorState } from "@/components/common/States";
import { Login } from "@/pages/Login";
import { Dashboard } from "@/pages/Dashboard";
import { Agents } from "@/pages/Agents";
import { AgentDetail } from "@/pages/AgentDetail";
import { Endpoints } from "@/pages/Endpoints";
import { EndpointDetail } from "@/pages/EndpointDetail";
import { Members } from "@/pages/Members";
import { Settings } from "@/pages/Settings";
import { Audit } from "@/pages/Audit";
import { NotFound } from "@/pages/NotFound";
import { SessionProvider } from "@/context/SessionContext";
import { useAuthConfig, useMe } from "@/api/hooks";
import { ApiError } from "@/api/client";

function AppRoutes() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<Dashboard />} />
          <Route path="agents" element={<Agents />} />
          <Route path="agents/:id" element={<AgentDetail />} />
          <Route path="endpoints" element={<Endpoints />} />
          <Route path="endpoints/:id" element={<EndpointDetail />} />
          <Route path="members" element={<Members />} />
          <Route path="settings" element={<Settings />} />
          <Route path="audit" element={<Audit />} />
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
        <ErrorState error={authConfig.error ?? new Error("Could not reach the control plane.")} />
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
    const unauthenticated = me.isError && me.error instanceof ApiError && me.error.status === 401;
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
