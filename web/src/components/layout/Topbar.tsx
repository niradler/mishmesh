import { Check, ChevronsUpDown, LogOut, Moon, Sun, User } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Badge } from "@/components/ui/badge";
import { useSession } from "@/context/SessionContext";
import { useTheme } from "@/context/ThemeContext";
import { useLogout } from "@/api/hooks";

export function Topbar() {
  const { me, authConfig, currentOrgId, setCurrentOrgId, currentMembership, role } = useSession();
  const { theme, toggle } = useTheme();
  const logout = useLogout();
  const memberships = me?.memberships ?? [];
  const currentOrgName =
    currentMembership?.org_name ?? memberships.find((m) => m.org_id === currentOrgId)?.org_name ?? "Organization";

  return (
    <header className="flex h-14 items-center justify-between gap-3 border-b border-border bg-card/80 px-4 backdrop-blur md:px-6">
      <div className="flex items-center gap-3">
        {memberships.length > 0 ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="gap-2">
                <span className="max-w-[12rem] truncate">{currentOrgName}</span>
                {role && <Badge variant="muted" className="hidden sm:inline-flex">{role}</Badge>}
                <ChevronsUpDown className="h-3.5 w-3.5 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuLabel>Organizations</DropdownMenuLabel>
              {memberships.map((m) => (
                <DropdownMenuItem key={m.org_id} onSelect={() => setCurrentOrgId(m.org_id)}>
                  <span className="flex-1 truncate">{m.org_name}</span>
                  {m.org_id === currentOrgId && <Check className="h-4 w-4" />}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          <Badge variant="muted">{currentOrgName}</Badge>
        )}
      </div>

      <div className="flex items-center gap-1.5">
        <Button variant="ghost" size="icon" onClick={toggle} aria-label="Toggle theme">
          {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        </Button>

        {authConfig.auth_enabled && me ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="gap-2">
                <span className="flex h-6 w-6 items-center justify-center rounded-full bg-secondary text-xs font-medium">
                  {(me.name || me.email).slice(0, 1).toUpperCase()}
                </span>
                <span className="hidden max-w-[10rem] truncate sm:inline">{me.name || me.email}</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel className="font-normal">
                <div className="flex flex-col">
                  <span className="text-sm font-medium text-foreground">{me.name || "User"}</span>
                  <span className="text-xs text-muted-foreground">{me.email}</span>
                </div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                className="text-destructive focus:text-destructive"
                onSelect={() => logout.mutate(undefined, { onSuccess: () => window.location.reload() })}
              >
                <LogOut className="h-4 w-4" />
                Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          <Badge variant="outline" className="gap-1.5">
            <User className="h-3 w-3" /> headless
          </Badge>
        )}
      </div>
    </header>
  );
}
