import * as React from "react";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AppHeader } from "@/components/app-header";
import { Palette } from "@/components/palette";
import { ScreenBoundary } from "@/components/screen-boundary";
import { Sidebar } from "@/components/sidebar";
import { SignIn } from "@/components/sign-in";
import { NotBuilt } from "@/screens/not-built";
import { NotFound } from "@/screens/not-found";
import { type Location, navigate, routeOf, useRouter } from "@/lib/router";
import { landing, serverBadge, visibleRoutes } from "@/lib/routes";
import { put } from "@/lib/handoff";
import { adminAreas, adminServers } from "@/lib/session";
import { useSession } from "@/lib/use-session";
import { useTheme } from "@/lib/theme";
import { useTimeZone } from "@/lib/timezone";
import type { YourDraft } from "@/lib/draft-save";

// Every screen loads when it is first opened, so the entry page carries the
// shell alone and stays inside its gzipped budget.
const Servers = React.lazy(() => import("@/screens/servers").then((m) => ({ default: m.Servers })));
const ServerPage = React.lazy(() => import("@/screens/server-page").then((m) => ({ default: m.ServerPage })));
const AddServer = React.lazy(() => import("@/screens/add-server").then((m) => ({ default: m.AddServer })));
const Sessions = React.lazy(() => import("@/screens/sessions").then((m) => ({ default: m.Sessions })));
const Users = React.lazy(() => import("@/screens/users").then((m) => ({ default: m.Users })));
const Transcripts = React.lazy(() => import("@/screens/transcripts").then((m) => ({ default: m.Transcripts })));
const Audit = React.lazy(() => import("@/screens/audit").then((m) => ({ default: m.Audit })));
const Roles = React.lazy(() => import("@/screens/roles").then((m) => ({ default: m.Roles })));
const RolePage = React.lazy(() => import("@/screens/role-page").then((m) => ({ default: m.RolePage })));
const NewRole = React.lazy(() => import("@/screens/new-role").then((m) => ({ default: m.NewRole })));
const Overview = React.lazy(() => import("@/screens/overview").then((m) => ({ default: m.Overview })));
const Settings = React.lazy(() => import("@/screens/settings").then((m) => ({ default: m.Settings })));
const Policies = React.lazy(() => import("@/screens/policies").then((m) => ({ default: m.Policies })));
const PolicyPage = React.lazy(() => import("@/screens/policy-page").then((m) => ({ default: m.PolicyPage })));
const NewPolicy = React.lazy(() => import("@/screens/new-policy").then((m) => ({ default: m.NewPolicy })));
const Approvals = React.lazy(() => import("@/screens/approvals").then((m) => ({ default: m.Approvals })));
const Drafts = React.lazy(() => import("@/screens/drafts").then((m) => ({ default: m.Drafts })));
const DraftPage = React.lazy(() => import("@/screens/draft-page").then((m) => ({ default: m.DraftPage })));

// DRAFTS_EVERY_MS is how often the sidebar reads the drafts that wait while
// the tab is shown; a route change reads it too.
const DRAFTS_EVERY_MS = 60000;

type ShellProps = {
  user: string;
  grants: string;
  location: Location;
  onSignOut: () => void;
};

// Shell is the signed-in frame: the sidebar, the header, the screen inside
// its boundary, the palette and the toaster.
function Shell({ user, grants, location, onSignOut }: ShellProps) {
  const [paletteOpen, setPaletteOpen] = React.useState(false);
  const { resolved } = useTheme();
  // One subscriber here repaints every absolute stamp when the zone flips.
  useTimeZone();

  // A session that holds a server's admin role reaches the MCP servers area
  // through the servers it administers, with no area grant of its own.
  const areas = adminAreas();
  const administers = adminServers();
  const visible = visibleRoutes(areas, administers);
  const route = routeOf(location);
  const reachable = route !== null && visible.some((r) => r.key === route.key);
  // The base path lands on the first built area, which a session with no
  // grant on it cannot open; such a session lands on its first visible
  // area instead, so a server admin opens the console on MCP servers.
  const first = visible[0] ? visible[0].key : null;
  const atBase = location.kind === "route" && route !== null && route.key === landing.key && location.rest.length === 0;
  React.useEffect(() => {
    if (atBase && !reachable && first) navigate(first, [], true);
  }, [atBase, reachable, first]);
  const rest = location.kind === "route" ? location.rest : [];
  const key: string = (route ? route.key : "not-found") + "/" + (rest[0] || "");

  // The count beside Drafts and the header's link to the person's working
  // draft load their wrappers on first use, so the drafts calls stay out of
  // the entry page. A failed read leaves the last answer.
  const [waiting, setWaiting] = React.useState("");
  const [yours, setYours] = React.useState<YourDraft | null>(null);
  const draftsShown = visible.some((r) => r.key === "drafts");
  React.useEffect(() => {
    if (!draftsShown) return;
    let alive = true;
    const read = () => {
      void import("@/lib/drafts-api").then((m) => m.waitingLabel()).then((n) => { if (alive) setWaiting(n); }, () => undefined);
      void import("@/lib/draft-save").then((m) => m.yourDraft()).then((y) => { if (alive) setYours(y); }, () => undefined);
    };
    read();
    const t = setInterval(() => { if (document.visibilityState !== "hidden") read(); }, DRAFTS_EVERY_MS);
    return () => { alive = false; clearInterval(t); };
  }, [draftsShown, key]);

  // The MCP servers, Roles and Policies areas have three screens under one
  // sidebar item each: the list, the wizard at <area>/new, and an object's
  // page at <area>/<id>/<tab>. Policies adds a second view of its list at
  // policies/by-role.
  // Approvals and Settings carry their tab in the address, and a request
  // opens from approvals/requests/<id>. Drafts carries its tab the same way,
  // and a draft's review page is drafts/<id>, an id being a number.
  let screen: React.ReactNode;
  if (!route) screen = <NotFound path={location.kind === "not-found" ? location.path : ""} />;
  else if (!reachable) screen = <NotBuilt route={route} reachable={false} grants={grants} administers={administers} first={visible[0] || null} />;
  else if (route.key === "servers" && rest[0] === "new") screen = <AddServer />;
  else if (route.key === "servers" && rest[0]) screen = <ServerPage id={rest[0]} tab={rest[1]} />;
  else if (route.key === "servers") screen = <Servers />;
  else if (route.key === "sessions") screen = <Sessions openID={rest[0]} />;
  else if (route.key === "users") screen = <Users openID={rest[0]} />;
  else if (route.key === "transcripts") screen = <Transcripts session={rest[0]} />;
  else if (route.key === "audit") screen = <Audit preset={rest[0] ? { session: rest[0] } : undefined} />;
  else if (route.key === "roles" && rest[0] === "new") screen = <NewRole />;
  else if (route.key === "roles" && rest[0]) screen = <RolePage id={rest[0]} tab={rest[1]} />;
  else if (route.key === "roles") screen = <Roles />;
  else if (route.key === "policies" && rest[0] === "by-role") screen = <Policies view="roles" />;
  else if (route.key === "policies" && rest[0] === "new") screen = <NewPolicy />;
  else if (route.key === "policies" && rest[0]) screen = <PolicyPage name={rest[0]} tab={rest[1]} />;
  else if (route.key === "policies") screen = <Policies view="sets" />;
  else if (route.key === "approvals" && route.built) screen = <Approvals tab={rest[0]} openID={rest[1]} />;
  else if (route.key === "drafts" && rest[0] && /^\d+$/.test(rest[0])) screen = <DraftPage id={rest[0]} />;
  else if (route.key === "drafts") screen = <Drafts tab={rest[0]} />;
  else if (route.key === "overview") screen = <Overview />;
  else if (route.key === "settings") screen = <Settings tab={rest[0]} />;
  else screen = <NotBuilt route={route} reachable grants={grants} administers={administers} first={null} />;

  return (
    <TooltipProvider delayDuration={200}>
      <div className="flex h-screen bg-background text-foreground">
        <Sidebar routes={visible} active={route ? route.key : null} servers={serverBadge(areas, administers)} drafts={draftsShown ? waiting : ""} />
        <div className="flex min-w-0 flex-1 flex-col">
          <AppHeader routes={visible} user={user} yours={yours} onOpenPalette={() => setPaletteOpen(true)} onSignOut={onSignOut} />
          <main className="min-h-0 flex-1 overflow-auto">
            <React.Suspense fallback={<p className="px-6 py-5 text-sm text-muted-foreground">Opening the page.</p>}>
              <ScreenBoundary key={key}>{screen}</ScreenBoundary>
            </React.Suspense>
          </main>
        </div>
      </div>
      <Palette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        routes={visible}
        onOpenServer={(app) => navigate("servers", [app.id])}
        onOpenUser={(u) => { put("users", { open: u.id }); navigate("users"); }}
        onOpenRole={(r) => navigate("roles", [r.id])}
      />
      <Toaster theme={resolved} closeButton />
    </TooltipProvider>
  );
}

export default function App() {
  const { state, signedIn, signOut } = useSession();
  const { location } = useRouter();

  if (state.kind === "booting") return <p className="p-6 text-sm text-muted-foreground">Resuming session</p>;
  if (state.kind === "signed-out") {
    const route = routeOf(location);
    return <SignIn reason={state.reason} returnTo={route ? route.label : landing.label} onAuthed={signedIn} />;
  }
  return <Shell user={state.user} grants={state.grants} location={location} onSignOut={signOut} />;
}
