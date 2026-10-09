import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/api/client";
import { useRegister } from "@/api/hooks";
import type { AuthConfig } from "@/api/types";

export function Register({ authConfig, onBack }: { authConfig: AuthConfig; onBack: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [inviteToken, setInviteToken] = useState(() => new URLSearchParams(window.location.search).get("invite") ?? "");
  const register = useRegister();
  const inviteRequired = authConfig.signup_mode === "invite" && !authConfig.bootstrap_required;

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    register.mutate({ name, email, password, invite_token: inviteToken || undefined }, { onSuccess: () => window.location.reload() });
  };

  const errorMessage =
    register.error instanceof ApiError
      ? register.error.message
      : register.error
        ? "Registration failed."
        : null;

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      {inviteRequired && (
        <p className="text-sm text-muted-foreground">
          Registration is by invitation. Use the email address your administrator invited.
        </p>
      )}
      <div className="space-y-1.5">
        <Label htmlFor="reg-invite">Invite token</Label>
        <Input
          id="reg-invite"
          autoComplete="off"
          value={inviteToken}
          onChange={(e) => setInviteToken(e.target.value.trim())}
          placeholder={inviteRequired ? "Required" : "Optional"}
          required={inviteRequired}
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="reg-name">Name</Label>
        <Input
          id="reg-name"
          autoComplete="name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Ada Lovelace"
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="reg-email">Email</Label>
        <Input
          id="reg-email"
          type="email"
          autoComplete="username"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@company.com"
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="reg-password">Password</Label>
        <Input
          id="reg-password"
          type="password"
          autoComplete="new-password"
          required
          minLength={8}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="At least 8 characters"
        />
      </div>
      {errorMessage && (
        <p className="text-sm text-destructive" role="alert">
          {errorMessage}
        </p>
      )}
      <Button type="submit" className="w-full" disabled={register.isPending}>
        {register.isPending ? "Creating account…" : "Create account"}
      </Button>
      <Button type="button" variant="ghost" className="w-full" onClick={onBack}>
        Back to sign in
      </Button>
    </form>
  );
}
