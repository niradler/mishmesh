import { useCallback } from "react";
import { useSearchParams } from "react-router-dom";

export function useTabParam<T extends string>(tabs: readonly T[], fallback: T): [T, (tab: string) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab");
  const active = tabs.find((tab) => tab === raw) ?? fallback;

  const setTab = useCallback(
    (tab: string) => {
      const next = new URLSearchParams(params);
      if (tab === fallback) next.delete("tab");
      else next.set("tab", tab);
      setParams(next, { replace: true });
    },
    [params, setParams, fallback],
  );

  return [active, setTab];
}
