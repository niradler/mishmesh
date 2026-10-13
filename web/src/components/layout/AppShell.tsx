import { Outlet, useMatch } from "react-router-dom";
import { cn } from "@/lib/utils";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";

export function AppShell() {
  const isNetwork = useMatch("/network") !== null;

  return (
    <div className="flex h-full overflow-hidden bg-background">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className={cn("min-h-0 min-w-0 flex-1", isNetwork ? "overflow-hidden" : "overflow-y-auto")}>
          <div
            className={cn(
              "mx-auto w-full animate-fade-in px-4 py-6 md:px-8 md:py-8",
              isNetwork ? "flex h-full min-h-0 flex-col overflow-hidden" : "max-w-6xl",
            )}
          >
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
