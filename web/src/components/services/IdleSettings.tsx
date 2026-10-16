import { useState } from "react";
import { LabelWithHelp } from "@/components/common/HelpTip";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { HELP } from "@/lib/glossary";
import { IDLE_PRESETS } from "@/lib/services";

function durationUnit(seconds: number): number {
  if (seconds > 0 && seconds % 3600 === 0) return 3600;
  if (seconds > 0 && seconds % 60 === 0) return 60;
  return 1;
}

export function IdleSettings({
  seconds,
  onChange,
}: {
  seconds: number;
  onChange: (seconds: number) => void;
}): React.JSX.Element {
  const [custom, setCustom] = useState(
    () => !IDLE_PRESETS.some((preset) => Number(preset.value) === seconds),
  );
  const [unit, setUnit] = useState(() => durationUnit(seconds));
  let description =
    "Stops after this long without traffic. A request to a linked HTTP endpoint starts it again; the first request waits while it starts.";
  if (seconds === 0)
    description =
      "Disables automatic stopping. Once started, the service stays running until you stop it or it exits. This does not start it now.";
  if (seconds === -1)
    description =
      "Stops when no dependent service needs it and no request is active. Use this for shared services such as a database required by an app.";
  return (
    <fieldset className="space-y-3 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">Automatic scale-down</legend>
      <LabelWithHelp htmlFor="idle-preset" entry={HELP.idleTimeout}>
        When to stop
      </LabelWithHelp>
      <select
        id="idle-preset"
        className="h-10 w-full rounded-md border bg-background px-3"
        aria-describedby="idle-description"
        value={custom ? "custom" : String(seconds)}
        onChange={(event) => {
          const isCustom = event.target.value === "custom";
          setCustom(isCustom);
          if (isCustom) {
            const next = seconds > 0 ? seconds : 1800;
            setUnit(durationUnit(next));
            onChange(next);
          } else onChange(Number(event.target.value));
        }}
      >
        {IDLE_PRESETS.map((preset) => (
          <option key={preset.value} value={preset.value}>
            {preset.label}
          </option>
        ))}
        <option value="custom">After a custom idle period</option>
      </select>
      {custom && (
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="idle-duration">Idle for</Label>
            <Input
              id="idle-duration"
              type="number"
              min={1 / unit}
              max={604800 / unit}
              step="any"
              required
              aria-describedby="idle-description idle-limit"
              value={Number.isNaN(seconds) ? "" : seconds / unit}
              onChange={(event) =>
                onChange(
                  event.target.value === ""
                    ? Number.NaN
                    : Number(event.target.value) * unit,
                )
              }
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="idle-unit">Unit</Label>
            <select
              id="idle-unit"
              className="h-10 w-full rounded-md border bg-background px-3"
              value={unit}
              onChange={(event) => {
                const nextUnit = Number(event.target.value);
                onChange((seconds / unit) * nextUnit);
                setUnit(nextUnit);
              }}
            >
              <option value={1}>Seconds</option>
              <option value={60}>Minutes</option>
              <option value={3600}>Hours</option>
            </select>
          </div>
        </div>
      )}
      <p
        id="idle-description"
        className="text-xs leading-relaxed text-muted-foreground"
      >
        {description}
      </p>
      {custom && (
        <p id="idle-limit" className="text-xs text-muted-foreground">
          From 1 second to 7 days, in whole seconds.
        </p>
      )}
      {seconds > 0 && (
        <p className="text-xs text-muted-foreground">
          Active requests and dependent services keep it running. Regular
          polling can prevent scale-down.
        </p>
      )}
    </fieldset>
  );
}
