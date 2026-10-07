import * as React from "react";
import { UserRoundIcon, ArrowUpRightIcon } from "lucide-react";
import { StrazaMark } from "@/components/straza-mark";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { type Route, type RouteKey, draftsBadgeTitle, serversBadgeTitle, groupedRoutes } from "@/lib/routes";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { type VersionInfo, version as readVersion } from "@/lib/public";
import { cn } from "@/lib/utils";

type Props = {
  routes: Route[];
  active: RouteKey | null;
  // servers is how many MCP servers this session administers, badged on the
  // MCP servers row. It is 0 for a session whose own grants open the area,
  // and the badge is then not drawn at all.
  servers?: number;
  // drafts is the count of drafts that wait, as the Drafts row draws it,
  // "" for none or before the first read.
  drafts?: string;
};

// Sidebar is the left rail: the brand, the areas the
// person's grants cover, the active one marked by fill and weight, and a
// footer with the profile word and the version string from GET /version.
export function Sidebar({ routes, active, servers = 0, drafts = "" }: Props) {
  const [version, setVersion] = React.useState<VersionInfo | null>(null);
  React.useEffect(() => {
    let live = true;
    void readVersion().then((v) => { if (live) setVersion(v); });
    return () => { live = false; };
  }, []);

  return (
    <aside className="console-sidebar flex h-full w-[220px] shrink-0 flex-col border-r border-border bg-card" data-sidebar>
      <div className="flex h-14 items-center gap-2.5 border-b border-border px-5 text-[15px] font-semibold tracking-wide text-foreground">
        <StrazaMark className="size-6 shrink-0" />
        STRAZA
      </div>
      <nav aria-label="Areas" className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-3 py-5">
        {groupedRoutes(routes).map((group) => <section key={group.label} className="nav-group" aria-label={group.label}>
          <h2 className="nav-group-label">{group.label}</h2>
          {group.routes.map((r) => {
          const on = r.key === active;
          return (
            <a
              key={r.key}
              href={pathFor(r.key)}
              aria-current={on ? "page" : undefined}
              className={cn(
                "flex min-h-9 items-center gap-3 rounded-md px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                on ? "bg-accent-bg font-semibold text-link [&>svg]:text-link" : "text-text-2 hover:bg-secondary/60 hover:text-foreground",
              )}
              onClick={(e) => {
                if (!isPlainClick(e)) return;
                e.preventDefault();
                navigate(r.key);
              }}
            >
              <r.icon className="size-[18px] shrink-0" aria-hidden="true" />
              {r.label}
              {servers > 0 && r.key === "servers" && (
                <Badge variant="secondary" className="ml-auto rounded-md font-mono text-[12px] font-normal" title={serversBadgeTitle(servers)} data-servers-badge>
                  {servers}
                </Badge>
              )}
              {drafts && r.key === "drafts" && (
                <Badge variant="secondary" className="ml-auto rounded-md font-mono text-[12px] font-normal" title={draftsBadgeTitle(drafts)} data-drafts-badge>
                  {drafts}
                </Badge>
              )}
            </a>
          );
        })}</section>)}
      </nav>
      <div className="personal-destination"><a href="/self-service/" data-self-service><UserRoundIcon aria-hidden="true" /><strong>Self-service</strong><ArrowUpRightIcon aria-hidden="true" /><span>Your requests, credentials and approval devices</span></a></div>
      <div className="mt-auto flex flex-col items-start gap-1.5 border-t border-border px-5 py-3 text-[13px] text-muted-foreground" data-version>
        {version ? (
          <>
            <Badge variant="secondary" className="rounded-md font-mono text-[12px] font-normal">{version.profile || "no profile"}</Badge>
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="block max-w-full cursor-default truncate font-mono text-[12px]">{"strazad " + version.version}</span>
              </TooltipTrigger>
              <TooltipContent>{version.version + " · " + version.commit + " · " + version.go}</TooltipContent>
            </Tooltip>
          </>
        ) : (
          <span className="font-mono">strazad</span>
        )}
      </div>
    </aside>
  );
}
