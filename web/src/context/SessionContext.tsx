import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react";
import { useSwitchOrg } from "@/api/hooks";
import type { AuthConfig, Me, Membership, Role } from "@/api/types";

interface SessionValue {
  authConfig: AuthConfig;
  me: Me | null;
  currentOrgId: string | undefined;
  setCurrentOrgId: (id: string) => void;
  currentMembership: Membership | null;
  role: Role | null;
  canWrite: boolean;
  isOwnerOrAdmin: boolean;
}

const SessionContext = createContext<SessionValue | null>(null);

export function SessionProvider({
  authConfig,
  me,
  children,
}: {
  authConfig: AuthConfig;
  me: Me | null;
  children: ReactNode;
}) {
  const memberships = me?.memberships ?? [];
  const currentOrgId = me?.active_org_id ?? memberships[0]?.org_id;
  const switchOrg = useSwitchOrg();

  const setCurrentOrgId = useCallback(
    (id: string) => {
      if (id === currentOrgId) return;
      switchOrg.mutate(id, { onSuccess: () => window.location.reload() });
    },
    [currentOrgId, switchOrg],
  );

  const value = useMemo<SessionValue>(() => {
    const currentMembership = memberships.find((m) => m.org_id === currentOrgId) ?? null;
    const role = currentMembership?.role ?? null;
    const isOwnerOrAdmin = role === "owner" || role === "admin";
    return {
      authConfig,
      me,
      currentOrgId,
      setCurrentOrgId,
      currentMembership,
      role,
      canWrite: !authConfig.auth_enabled || isOwnerOrAdmin,
      isOwnerOrAdmin: !authConfig.auth_enabled || isOwnerOrAdmin,
    };
  }, [authConfig, me, memberships, currentOrgId, setCurrentOrgId]);

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within SessionProvider");
  return ctx;
}
