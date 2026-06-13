import { NavLink } from "react-router-dom";
import { Activity, KeyRound, LayoutGrid, ListTree, ScrollText, Settings, Users } from "lucide-react";
import { cn } from "@/lib/utils";

const NAV = [
  { to: "/", label: "Dashboard", icon: LayoutGrid, end: true },
  { to: "/agents", label: "Agents", icon: KeyRound },
  { to: "/endpoints", label: "Endpoints", icon: ListTree },
  { to: "/members", label: "Members", icon: Users },
  { to: "/audit", label: "Audit", icon: ScrollText },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function Sidebar() {
  return (
    <aside className="hidden w-60 shrink-0 flex-col border-r border-border bg-card md:flex">
      <div className="flex h-14 items-center gap-2 border-b border-border px-5">
        <div className="flex h-7 w-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <Activity className="h-4 w-4" />
        </div>
        <span className="font-semibold tracking-heading">mishmesh</span>
      </div>
      <nav className="flex-1 space-y-0.5 p-3">
        {NAV.map(({ to, label, icon: Icon, end }) => (
          <NavLink
            key={to}
            to={to}
            end={end}
            className={({ isActive }) =>
              cn(
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors",
                isActive
                  ? "bg-secondary text-foreground"
                  : "text-muted-foreground hover:bg-accent hover:text-foreground",
              )
            }
          >
            <Icon className="h-4 w-4" />
            {label}
          </NavLink>
        ))}
      </nav>
      <div className="border-t border-border p-4 text-xs text-muted-foreground">
        <p>Self-hosted tunnel platform</p>
      </div>
    </aside>
  );
}
