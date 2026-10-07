import * as React from "react";
import { CheckIcon, ChevronDownIcon, CommandIcon, MenuIcon, MonitorIcon, MoonIcon, SunIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuGroup, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { YourDraft } from "@/lib/draft-save";
import { type Readiness, readiness } from "@/lib/public";
import { type ThemeChoice, setTheme, useTheme } from "@/lib/theme";
import { setTimeZone, useTimeZone } from "@/lib/timezone";
import { groupedRoutes, type Route } from "@/lib/routes";
import { navigate } from "@/lib/router";
import { cn } from "@/lib/utils";

type Props = {
  routes?: Route[];
  user: string;
  // yours is the person's open working draft, which the header links to.
  yours?: YourDraft | null;
  onOpenPalette: () => void;
  onSignOut: () => void;
};

const POLL_MS = 10000;

// HealthDot polls /readyz every 10 s: Healthy in green, Degraded in amber
// with the failing component names on hover, Unknown in violet when
// strazad did not answer, never a claim it is fine.
function HealthDot() {
  const [ready, setReady] = React.useState<Readiness | null>(null);
  React.useEffect(() => {
    let live = true;
    const probe = () => void readiness().then((r) => { if (live) setReady(r); });
    probe();
    const t = setInterval(probe, POLL_MS);
    return () => { live = false; clearInterval(t); };
  }, []);
  const state = ready ? ready.state : "unreachable";
  const word = state === "ok" ? "Healthy" : state === "degraded" ? "Degraded" : "Unknown";
  const dot = state === "ok" ? "bg-ok" : state === "degraded" ? "bg-warn" : "bg-unknown";
  const failing = ready && state === "degraded"
    ? Object.entries(ready.components).filter(([, v]) => v !== "ok").map(([k]) => k).join(", ")
    : "";
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="flex cursor-default items-center gap-2 text-[13px] text-text-2" data-health={state}>
          <span className={cn("size-2 rounded-full", dot)} aria-hidden="true" />
          {word}
        </span>
      </TooltipTrigger>
      <TooltipContent>{"/readyz, polled every 10 s." + (failing ? " Failing: " + failing + "." : "")}</TooltipContent>
    </Tooltip>
  );
}

const THEMES: { choice: ThemeChoice; label: string; icon: typeof SunIcon }[] = [
  { choice: "system", label: "System", icon: MonitorIcon },
  { choice: "light", label: "Light", icon: SunIcon },
  { choice: "dark", label: "Dark", icon: MoonIcon },
];

// ThemeMenu is the icon button that opens the three theme choices with a
// check on the current one.
function ThemeMenu() {
  const { choice } = useTheme();
  const now = THEMES.find((t) => t.choice === choice) || THEMES[0];
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={"Theme: " + now.label}>
          <now.icon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {THEMES.map((t) => (
          <DropdownMenuItem key={t.choice} onSelect={() => setTheme(t.choice)} aria-checked={t.choice === choice} role="menuitemradio">
            <t.icon />
            {t.label}
            {t.choice === choice && <CheckIcon className="ml-auto" aria-hidden="true" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// AppHeader holds page search, the link to the person's working draft,
// readiness, timezone, theme and sign-out. Narrow layouts also expose the
// permitted navigation and Self-service here.
export function AppHeader({ routes = [], user, yours, onOpenPalette, onSignOut }: Props) {
  const zone = useTimeZone();
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border bg-card px-4" data-app-header>
      <DropdownMenu>
        <DropdownMenuTrigger asChild><Button variant="outline" size="icon-sm" className="mobile-navigation" aria-label="Open navigation"><MenuIcon /></Button></DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="max-h-[80vh] overflow-y-auto">
          {groupedRoutes(routes).map((group) => <DropdownMenuGroup key={group.label}><DropdownMenuLabel>{group.label}</DropdownMenuLabel>{group.routes.map((r) => <DropdownMenuItem key={r.key} onSelect={() => navigate(r.key)}><r.icon />{r.label}</DropdownMenuItem>)}</DropdownMenuGroup>)}
          <DropdownMenuSeparator /><DropdownMenuItem asChild><a href="/self-service/">Self-service</a></DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button variant="outline" size="sm" className="page-search" onClick={onOpenPalette} aria-label="Find a page, server, user, role or policy">
        <CommandIcon /><span>Find a page, server, user, role or policy</span>
        <kbd className="ml-1 rounded border border-border bg-background px-1 font-mono text-[11px] text-muted-foreground">Ctrl K</kbd>
      </Button>
      <div className="ml-auto flex items-center gap-3">
        {yours && (
          <Button variant="ghost" size="sm" className="text-[13px] text-link" title="Your open draft" onClick={() => navigate("drafts", [yours.id])} data-your-draft={yours.id}>
            {"Your draft · " + yours.changes + (yours.changes === 1 ? " change" : " changes")}
          </Button>
        )}
        <HealthDot />
        <Tooltip>
          <TooltipTrigger asChild>
            <Button variant="outline" size="sm" className="font-mono text-[13px]" onClick={() => setTimeZone(zone === "utc" ? "local" : "utc")} data-zone={zone}>
              {zone === "utc" ? "UTC" : "local"}
            </Button>
          </TooltipTrigger>
          <TooltipContent>
            {"Timestamps are shown in " + (zone === "utc" ? "UTC" : "this browser's local time") + ". Click to switch; the choice is remembered in this browser."}
          </TooltipContent>
        </Tooltip>
        <ThemeMenu />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" data-user-menu>
              {user}
              <ChevronDownIcon aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onSignOut}>Sign out</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}
