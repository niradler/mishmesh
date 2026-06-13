import { useState, type FormEvent } from "react";
import { Activity } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/api/client";
import { useLogin } from "@/api/hooks";
import type { AuthConfig } from "@/api/types";
import { API_BASE } from "@/api/client";

const GoogleIcon = () => (
  <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden="true">
    <path
      fill="currentColor"
      d="M21.35 11.1H12v3.2h5.35c-.23 1.5-1.6 4.4-5.35 4.4-3.22 0-5.85-2.67-5.85-5.95S8.78 6.8 12 6.8c1.84 0 3.07.78 3.78 1.46l2.58-2.49C16.7 4.18 14.55 3.3 12 3.3 6.92 3.3 2.8 7.4 2.8 12.75S6.92 22.2 12 22.2c5.5 0 9.13-3.86 9.13-9.3 0-.62-.07-1.1-.18-1.8z"
    />
  </svg>
);

export function Login({ authConfig }: { authConfig: AuthConfig }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const login = useLogin();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ email, password }, { onSuccess: () => window.location.reload() });
  };

  const errorMessage =
    login.error instanceof ApiError
      ? login.error.message
      : login.error
        ? "Sign in failed."
        : null;

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden bg-background px-4 py-12">
      <div className="pointer-events-none absolute inset-0 grid-blueprint opacity-60" />
      <div className="pointer-events-none absolute inset-0 bg-gradient-to-b from-background/0 via-background/60 to-background" />
      <div className="relative w-full max-w-sm animate-fade-in">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-primary text-primary-foreground">
            <Activity className="h-5 w-5" />
          </div>
          <div>
            <h1 className="text-2xl font-semibold tracking-heading">mishmesh</h1>
            <p className="mt-1 text-sm text-muted-foreground">Sign in to your control plane</p>
          </div>
        </div>

        <div className="rounded-card border border-border bg-card p-6 shadow-sm">
          {authConfig.password_enabled && (
            <form onSubmit={onSubmit} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="email">Email</Label>
                <Input
                  id="email"
                  type="email"
                  autoComplete="username"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@company.com"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="••••••••"
                />
              </div>
              {errorMessage && (
                <p className="text-sm text-destructive" role="alert">
                  {errorMessage}
                </p>
              )}
              <Button type="submit" className="w-full" disabled={login.isPending}>
                {login.isPending ? "Signing in…" : "Sign in"}
              </Button>
            </form>
          )}

          {authConfig.password_enabled && authConfig.google_enabled && (
            <div className="my-5 flex items-center gap-3 text-xs text-muted-foreground">
              <span className="h-px flex-1 bg-border" />
              OR
              <span className="h-px flex-1 bg-border" />
            </div>
          )}

          {authConfig.google_enabled && (
            <Button variant="outline" className="w-full" asChild>
              <a href={`${API_BASE}/auth/google/start`}>
                <GoogleIcon />
                Continue with Google
              </a>
            </Button>
          )}

          {!authConfig.password_enabled && !authConfig.google_enabled && (
            <p className="text-center text-sm text-muted-foreground">
              No sign-in methods are enabled. Configure authentication on the server.
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
