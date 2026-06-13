import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
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

const STORAGE_KEY = "mishmesh.org";

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
  const [currentOrgId, setOrgState] = useState<string | undefined>(() => {
    const stored = typeof window !== "undefined" ? window.localStorage.getItem(STORAGE_KEY) : null;
    if (stored && memberships.some((m) => m.org_id === stored)) return stored;
    return memberships[0]?.org_id;
  });

  const setCurrentOrgId = useCallback((id: string) => {
    setOrgState(id);
    window.localStorage.setItem(STORAGE_KEY, id);
  }, []);

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
