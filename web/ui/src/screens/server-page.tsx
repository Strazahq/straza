import * as React from "react";
import { DownloadIcon, Loader2Icon, LockIcon, RefreshCwIcon, ServerOffIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ToneBadge } from "@/components/overview-parts";
import { PageHead } from "@/components/page-head";
import { ServerActivity } from "@/components/server-activity";
import type { ChangeWhich } from "@/components/server-change-sheet";
import { ServerOverview } from "@/components/server-overview";
import { ServerRolesTab } from "@/components/server-roles-tab";
import { NoRole, RoleChip, StatusBadge } from "@/components/status-badge";
import { TestACall } from "@/components/test-a-call";
import { SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { type ApiError, type AppRow, type BindingRow, type PreviewEntry, type RoleRow, type ToolRow, appLogs, catalogPreview, disableApp, enableApp, getConfig, listApps, listAudit, listBindings, listRoles, listTools, recheckApp } from "@/lib/api";
import { manifestYAML } from "@/lib/manifest";
import {
  BANNER, FROM_SUMMARY, LAST_CALL_NOT_READABLE, NOT_CHECKED_YET, NO_DESCRIPTION, NO_ROLE_ACCESS, REACH_NOT_READABLE, ROLE_ACCESS_NOT_READABLE, SINCE_NOT_RECORDED,
  fileParts, lockedServerTitle, sinceWhile, toolsWhile,
} from "@/lib/server-words";
import { navigate } from "@/lib/router";
import { REMOVE_OPEN, SAVE_DRAFT, removeTitle, removedToast } from "@/lib/save-words";
import { TAB_ROLES, TOOLS_TAB_LINE } from "@/lib/server-roles-words";
import { adminAreas } from "@/lib/session";
import { notify } from "@/lib/notify";
import { POLICY_HELP, aboutServer, absTime, noToolWords, probeSay, probeWords, reachedBy, relTimeText, removedAt, runWord } from "@/lib/words";
import { downloadText } from "@/lib/utils";

// The Change sheets load when one is first opened, so their code stays out
// of the entry page's gzipped budget.
const ChangeSheet = React.lazy(() => import("@/components/server-change-sheet").then((m) => ({ default: m.ChangeSheet })));

type Props = { id: string; tab?: string };

const TABS = ["overview", "tools", "roles", "activity", "manifest"] as const;
type Tab = (typeof TABS)[number];

const CAPS = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";


type LastCall = { kind: "none" } | { kind: "unread" } | { kind: "closed" } | { kind: "call"; user: string; tool: string; t: string; verdict: string };
type Data = { app: AppRow; tools: ToolRow[]; bindings: BindingRow[]; roles: RoleRow[]; last: LastCall; reachReadable: boolean };
type State = { kind: "loading" } | { kind: "missing" } | { kind: "locked"; message: string } | { kind: "error"; message: string } | { kind: "ready"; data: Data; problem: string | null; lastRead: Date };
type Problem = { subject: string; unreachable: boolean; sentence: string };
type Busy = "recheck" | "pause" | "enable" | null;

// Run is one outcome in a tool's Policy cell: the word on the chip and the
// muted line under it. RUN_TONE is the trust hue of the three outcomes a
// policy decides and of a preview that could not be read. Any other word,
// a server that is not running or a role without access, is a plain chip.
type Run = { word: string; line: string };
const RUN_TONE: Record<string, string> = { allowed: "ok", "needs approval": "warn", denied: "danger", unknown: "unknown" };

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// lastCallOf reads the newest gateway decision about this server out of the
// audit rows, which the q filter matches loosely, so the server is checked
// on the record itself, and a call from before the newest removal of a
// server with this name belongs to that server. The name shown is the
// username the list resolved, since the record's own user field is an id.
function lastCallOf(rows: { ce: string; username?: string }[], name: string): LastCall {
  const cut = removedAt(rows, name);
  for (const r of rows) {
    let ce: { type?: string; time?: string; data?: Record<string, unknown> } | null = null;
    try { ce = JSON.parse(r.ce); } catch { continue; }
    if (!ce || ce.type !== "straza.audit.mcp" || (cut && Date.parse(ce.time || "") <= cut)) continue;
    const d = ce.data || {};
    const about = aboutServer(d, name);
    if (!about.mine) continue;
    const effect = String(d.effect || "");
    const verdict = effect === "allow" ? "allowed" : effect === "approve" || effect === "confirm" ? "needs approval" : "denied";
    return { kind: "call", user: r.username || String(d.user || "") || "a session", tool: about.tool, t: ce?.time || "", verdict };
  }
  return { kind: "none" };
}

function lastCallWords(last: LastCall): string {
  if (last.kind === "call") return "Last call: " + last.user + " called " + last.tool + " " + relTimeText(last.t) + ", " + last.verdict + ".";
  if (last.kind === "unread") return "Last call: not read, the audit chain did not answer.";
  if (last.kind === "closed") return LAST_CALL_NOT_READABLE;
  return "Last call: none recorded.";
}

// Banner is the status in words only: the badge, the state, the tool count
// or the reason, since when, the last check, who reaches it, the last call.
function Banner({ app, reach, last, reachReadable }: { app: AppRow; reach: string[]; last: LastCall; reachReadable: boolean }) {
  const status = app.status || "unknown";
  // A running or degraded server is answered from its live instance, which
  // omits an empty tool list. Any other status is answered from the stored
  // row, which carries no tool list and no status time at all.
  const live = status === "running" || status === "degraded";
  const tools = app.tools !== undefined ? app.tools.length : live ? 0 : toolsWhile(status);
  const since = app.status_since ? absTime(app.status_since) : live ? SINCE_NOT_RECORDED : sinceWhile(status);
  return (
    <section className="server-status" data-server-status={status} aria-label={BANNER.title}>
      <header><h2>{BANNER.title}</h2><StatusBadge status={status} />{app.paused && <Badge variant="outline" className="text-warn">{BANNER.paused}</Badge>}</header>
      <dl>
        <div><dt>{BANNER.tools}</dt><dd>{tools}</dd></div>
        <div><dt>{BANNER.checked}</dt><dd title={app.last_probe_at ? absTime(app.last_probe_at) : undefined}>{app.last_probe_at ? relTimeText(app.last_probe_at) : NOT_CHECKED_YET}</dd></div>
        <div><dt>{status === "running" ? BANNER.runningSince : BANNER.statusSince}</dt><dd>{since}</dd></div>
        <div><dt>{BANNER.access}</dt><dd>{reach.length ? reach.join(", ") + (!reachReadable ? FROM_SUMMARY : "") : !reachReadable ? ROLE_ACCESS_NOT_READABLE : NO_ROLE_ACCESS}</dd></div>
        <div className="server-last-call"><dt>{BANNER.lastCall}</dt><dd>{lastCallWords(last).replace(/^Last call: /, "")}</dd></div>
      </dl>
      {status !== "running" && app.detail && <details className="server-diagnostics" open={status === "failed"}><summary>{BANNER.diagnostics}</summary><p>{probeWords(app.detail, app.url)}</p></details>}
    </section>
  );
}

// ToolsTab answers "who can call X": each tool with who reaches it and, from
// the catalog preview per reaching role, how a call would run.
// ToolsTab answers who can call each exposed tool. Which tools are exposed
// is a manifest setting, so the tab carries the same door as the Settings
// card, onChange, and none for a server the console cannot change.
function ToolsTab({ app, tools, bindings, reachReadable, globalAdmin, onChange }: { app: AppRow; tools: ToolRow[]; bindings: BindingRow[]; reachReadable: boolean; globalAdmin: boolean; onChange: (() => void) | null }) {
  // No previews are asked for when the reach is not readable: the policy
  // area would refuse them the same way.
  const reach = reachReadable ? bindings.filter((b) => b.app === app.name).map((b) => b.role).filter((r, i, xs) => xs.indexOf(r) === i).sort() : [];
  const reachKey = reach.join(",");
  const [previews, setPreviews] = React.useState<Record<string, Record<string, PreviewEntry> | null>>({});
  React.useEffect(() => {
    let alive = true;
    for (const role of reachKey ? reachKey.split(",") : []) {
      catalogPreview(role, app.name).then(
        (res) => {
          if (!alive) return;
          const byTool: Record<string, PreviewEntry> = {};
          for (const e of res?.entries || []) if (e.tool) byTool[e.tool] = e;
          setPreviews((p) => ({ ...p, [role]: byTool }));
        },
        () => { if (alive) setPreviews((p) => ({ ...p, [role]: null })); },
      );
    }
    return () => { alive = false; };
  }, [reachKey, app.name]);

  const known = tools.filter((t) => t.app === app.name);
  const names = known.length ? known : (app.tools || []).map((n) => ({ id: n, app: app.name, app_id: app.id, name: n, description: "" }));
  const status = app.status || "unknown";
  // howItRuns answers a plain sentence where no outcome can be said, else
  // the outcomes of the reaching roles folded by word. The line of an
  // outcome names the policy sets that decide it, then the roles it holds
  // for when the reaching roles do not share one outcome.
  const howItRuns = (tool: string): string | Run[] => {
    if (!reachReadable) return "not readable with this account";
    const rs = reachedBy(bindings, app.name, tool);
    if (!rs.length) return "does not exist for any session";
    const folded: { word: string; sets: string[]; roles: string[] }[] = [];
    for (const r of rs) {
      const p = previews[r];
      if (p === undefined) return "reading";
      const e = p ? p[tool] : undefined;
      const word = p === null ? "unknown" : runWord(e && { status: e.status }) || "no access";
      // The set is named where a policy gates or denies the tool, as runWord names it.
      const set = e && (e.status === "approve_gated" || e.status === "hidden_policy") ? e.setName : "";
      let run = folded.find((f) => f.word === word);
      if (!run) folded.push((run = { word, sets: [], roles: [] }));
      run.roles.push(r);
      if (set && !run.sets.includes(set)) run.sets.push(set);
    }
    return folded.map((f) => ({ word: f.word, line: [f.sets.join(", "), folded.length > 1 ? "for " + f.roles.join(", ") : ""].filter(Boolean).join(" ") }));
  };
  const offered = app.offered?.length;
  const exposedWords = offered === undefined ? "" : offered === names.length ? "Every tool the server lists is exposed." : names.length + " of the " + offered + " tools the server lists are exposed.";
  return (
    <div>
      {(exposedWords || onChange || !globalAdmin) && (
        <div className="mb-3 flex flex-wrap items-center gap-2" data-tools-head>
          <p className="min-w-0 flex-1 text-[13px] text-muted-foreground">{exposedWords}</p>
          {!globalAdmin && <span className="text-[13px] text-muted-foreground" data-roles-door>{TOOLS_TAB_LINE}</span>}
          {onChange && <Button variant="outline" size="sm" aria-label="Change the exposed tools" onClick={onChange}>Change the exposed tools</Button>}
        </div>
      )}
      <div className="overflow-x-auto rounded-md border border-border">
        <Table data-server-tools>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-[20%]">Tool</TableHead>
              <TableHead>What it does</TableHead>
              <TableHead className="w-[18%]">Reached by</TableHead>
              <TableHead className="w-[24%]"><span className="inline-flex items-center gap-1">Policy<HelpTip label="Policy" text={POLICY_HELP} /></span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {names.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="whitespace-normal text-muted-foreground">
                  {noToolWords(status, app.detail, app.url)}
                </TableCell>
              </TableRow>
            )}
            {names.map((t) => {
              const rs = reachedBy(bindings, app.name, t.name);
              const how = howItRuns(t.name);
              return (
                <TableRow key={t.name} className="text-sm">
                  <TableCell className="align-top font-mono text-foreground">{t.name}</TableCell>
                  <TableCell className="whitespace-normal align-top text-text-2">{t.description || <span className="text-muted-foreground">The server gave no description.</span>}</TableCell>
                  <TableCell className="align-top">{!reachReadable ? <span className="text-muted-foreground">not readable</span> : rs.length ? rs.map((r) => <RoleChip key={r} name={r} />) : <NoRole text="no role" />}</TableCell>
                  <TableCell className="whitespace-normal align-top text-text-2">
                    {typeof how === "string" ? how : (
                      <div className="flex flex-col items-start gap-1">
                        {how.map((run) => (
                          <React.Fragment key={run.word}>
                            <ToneBadge tone={RUN_TONE[run.word] || "plain"} word={run.word} />
                            {run.line && <span className="font-mono text-[13px] leading-snug text-muted-foreground" data-policy-sets>{run.line}</span>}
                          </React.Fragment>
                        ))}
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
      <p className="mt-1.5 text-[13px] text-muted-foreground" data-reach-note>{reachReadable ? 'This table answers "who can call X". Nothing is reachable until a role has access to it. Policies add the gates.' : REACH_NOT_READABLE}</p>
    </div>
  );
}

// ManifestTab shows the app.yaml as installed and names the change path:
// the Change buttons for a stored manifest they can edit, with the file
// that proposes drafts for a server a file names, and the install command
// for a row that carries none, whose Overview has no Change button either.
function ManifestTab({ app }: { app: AppRow }) {
  const yaml = app.manifest ? manifestYAML(app.manifest) : null;
  const file = app.file ? fileParts(app.file) : null;
  const download = () => { if (yaml) downloadText(app.name + ".yaml", yaml, "application/yaml"); };
  return (
    <section>
      <div className="mb-1.5 flex items-center gap-2">
        <span className={CAPS}>the manifest as installed (app.yaml)</span>
        <Button variant="outline" size="sm" className="ml-auto" onClick={download} aria-disabled={!yaml || undefined} title={yaml ? undefined : "There is no stored manifest to download."}>
          <DownloadIcon /> Download app.yaml
        </Button>
      </div>
      {yaml ? (
        <pre className="m-0 overflow-x-auto rounded-md border border-border bg-card px-4 py-3 font-mono text-[13px] leading-relaxed text-text-2">{yaml}</pre>
      ) : (
        <p className="rounded-md border border-border bg-card px-4 py-3 text-sm text-text-2">
          {"The console cannot read a stored manifest for this server. The file you installed, or the apps directory, holds it; the runtime is " + app.runtime + (app.version ? ", version " + app.version : "") + "."}
        </p>
      )}
      {file && yaml ? (
        <p className="mt-1.5 text-[13px] text-muted-foreground" data-change-path>{"To change it, use the Change buttons on Overview, or edit " + file.base + (file.dir ? " in " + file.dir : "") + ", which proposes a draft a person publishes."}</p>
      ) : yaml ? (
        <p className="mt-1.5 text-[13px] text-muted-foreground" data-change-path>{"To change it, use the Change buttons on Overview. "}<code className={CODE}>{"strazactl apps install -f " + app.name + ".yaml"}</code>{" does the same from a file you keep."}</p>
      ) : (
        <p className="mt-1.5 text-[13px] text-muted-foreground" data-change-path>{"To change it, edit the file you installed and run "}<code className={CODE}>{"strazactl apps install -f " + app.name + ".yaml"}</code>{"."}</p>
      )}
    </section>
  );
}

// tolerated answers a list read, or the empty value with false when the
// server refused it for lack of an area grant; any other failure stays.
async function tolerated<T>(p: Promise<T>, empty: T): Promise<[T, boolean]> {
  try {
    return [await p, true];
  } catch (e) {
    if ((e as ApiError).status === 403) return [empty, false];
    throw e;
  }
}

// missingState tells a server this account does not administer from one
// that does not exist. strazad lists a server admin's own servers only, so
// a server outside the list is asked through its logs route, a read every
// admin of it may make: a 403 carries the refusal naming the admin role
// and the panel shows that sentence, anything else is not found.
async function missingState(id: string): Promise<State> {
  const areas = adminAreas();
  if (areas === null || areas.apps) return { kind: "missing" };
  try {
    await appLogs(id);
    return { kind: "missing" };
  } catch (e) {
    const err = e as ApiError;
    return err.status === 403 ? { kind: "locked", message: err.message } : { kind: "missing" };
  }
}

// ServerPage is one MCP server's page at /console/servers/<id>/<tab>: the
// head with every action, the status in words, and the four tabs, with
// Overview the default. The Change sheet for one Overview card opens over it.
export function ServerPage({ id, tab }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [busy, setBusy] = React.useState<Busy>(null);
  const [ask, setAsk] = React.useState<"pause" | "remove" | null>(null);
  const [problem, setProblem] = React.useState<Problem | null>(null);
  const [testOpen, setTestOpen] = React.useState(false);
  const [sheet, setSheet] = React.useState<ChangeWhich | null>(null);
  const [upstreamTimeout, setUpstreamTimeout] = React.useState<number | null>(null);
  const [, tick] = React.useState(0);
  const current: Tab = (TABS as readonly string[]).includes(tab || "") ? (tab as Tab) : "overview";

  const load = React.useCallback(async () => {
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    try {
      // The tools and bindings lists are apps-area reads that a server admin
      // holds no grant for, so a 403 on them leaves the page standing on the
      // row's own tool list and says the reach is not readable.
      // The roles list is read the same way: a server admin's answer holds
      // the roles their own servers own, and the Roles tab shows this
      // server's.
      const [apps, [tools, toolsRead], [bindings, bindingsRead], [roles]] = await Promise.all([
        listApps(), tolerated<ToolRow[]>(listTools(), []), tolerated<BindingRow[]>(listBindings(), []), tolerated<RoleRow[]>(listRoles(), []),
      ]);
      const app = apps.find((a) => a.id === id);
      if (!app) { setState(await missingState(id)); return; }
      let last: LastCall = { kind: "unread" };
      try {
        last = lastCallOf(await listAudit("q=" + encodeURIComponent(app.name) + "&order=desc&limit=100"), app.name);
      } catch (e) {
        // The banner says the chain was not read, or not readable by this
        // account when the audit area refused it.
        if ((e as ApiError).status === 403) last = { kind: "closed" };
      }
      setState({ kind: "ready", data: { app, tools, bindings, roles, last, reachReadable: toolsRead && bindingsRead }, problem: null, lastRead: new Date() });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const message = err.unreachable
        ? "The server could not be read because strazad did not answer. Check that it is running, then reload."
        : "The server could not be read: " + err.message + ". Reload to try again.";
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
    }
  }, [id]);

  React.useEffect(() => { void load(); }, [load]);
  // The Credential tab folded into Overview; an old link to it lands there
  // without leaving the old address in the back stack.
  React.useEffect(() => {
    if (tab === "credential") navigate("servers", [id, "overview"], true);
  }, [id, tab]);
  // The server-wide per-call timeout names the default a manifest without
  // its own falls back to. A config the session may not read leaves it
  // unnamed rather than failing the page.
  React.useEffect(() => {
    let alive = true;
    getConfig().then(
      (c) => { if (alive) setUpstreamTimeout(c?.apps?.upstream_timeout_seconds || null); },
      () => { /* the cards say "the server-wide default" without a number */ },
    );
    return () => { alive = false; };
  }, []);
  React.useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30000);
    return () => clearInterval(t);
  }, []);

  // refuse words a write the server refused or that never reached it, for
  // the block above the tabs and the toast that stays until closed.
  const refuse = (subject: string, err: ApiError) => {
    if (err.status === 401) return;
    const sentence = err.unreachable ? subject + " did not reach strazad. Check the connection, then try again."
      : err.status === 404 ? subject + " refused: the server no longer knows this MCP server. Reload the list."
        : subject + " refused: " + err.message.replace(/\.$/, "") + ".";
    setProblem({ subject, unreachable: err.unreachable, sentence });
    notify.failed(sentence);
  };

  // A recheck answer carries the probe fields but not reached_by, so only
  // those move over, the way the sheet merges them.
  const merge = (view: AppRow) => setState((s) => (s.kind === "ready"
    ? { ...s, data: { ...s.data, app: { ...s.data.app, status: view.status, detail: view.detail, tools: view.tools, last_probe_at: view.last_probe_at, last_healthy_at: view.last_healthy_at, status_since: view.status_since } } }
    : s));

  const act = async (what: Exclude<Busy, null>, subject: string, fn: (app: AppRow) => Promise<void>) => {
    if (busy || state.kind !== "ready") return;
    setBusy(what);
    setProblem(null);
    try {
      await fn(state.data.app);
    } catch (e) {
      refuse(subject, e as ApiError);
    } finally {
      setBusy(null);
    }
  };
  const recheck = () => act("recheck", "Recheck", async (app) => {
    const view = await recheckApp(app.id);
    merge(view);
    const said = probeSay(view);
    notify[said.tone](said.text);
  });
  const pause = () => act("pause", "Pause", async (app) => {
    await disableApp(app.id);
    notify.ok(app.name + " is paused. Its tools left every session's list.");
    await load();
  });
  const enable = () => act("enable", "Enable", async (app) => {
    await enableApp(app.id);
    notify.ok(app.name + " is enabled. Straza starts it and checks it now.");
    await load();
  });
  // Remove server publishes an App removal through the drafts, so the
  // publish asks for the server's name typed, and Save
  // draft adds the removal to the person's working draft.
  const name = state.kind === "ready" ? state.data.app.name : id;
  const remover = useDraftSave({ name, title: removeTitle(name), toast: removedToast(name), onPublished: () => navigate("servers") });
  const removal = [{ kind: "App" as const, name, op: "remove" as const, doc: "" }];

  if (state.kind === "loading") return <PageHead label="MCP servers" title="MCP server" description="Reading the server from strazad." />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label="MCP servers" title="MCP server" description="The server could not be read." />
        <div className="flex flex-col items-start gap-3 px-6 py-5">
          <FetchError subject="MCP server" detail={state.message} />
          <Button variant="outline" size="sm" onClick={() => void load()}>Reload</Button>
        </div>
      </>
    );
  }
  if (state.kind === "locked") {
    return (
      <>
        <PageHead label="MCP servers" title={id} description="A server this account does not administer." />
        <EmptyState icon={LockIcon} title={lockedServerTitle(id)} action={<Button variant="outline" onClick={() => navigate("servers")}>Open MCP servers</Button>}>
          {cap(state.message.replace(/\.?$/, "."))}
        </EmptyState>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label="MCP servers" title="Not found" description="The address names a server that is not in the list." />
        <EmptyState icon={ServerOffIcon} title="No server has this id." action={<Button variant="outline" onClick={() => navigate("servers")}>Open MCP servers</Button>}>
          {"The list strazad answered has no row with the id " + id + ": it was removed, or the address is stale."}
        </EmptyState>
      </>
    );
  }

  const { app, tools, bindings, last } = state.data;
  const status = app.status || "unknown";
  const stopped = status === "stopped" || status === "failed";
  // The row's own reached_by stands in when the bindings list was not
  // readable, so the banner never claims no role reaches a reached server.
  const reach = state.data.reachReadable
    ? bindings.filter((b) => b.app === app.name).map((b) => b.role).filter((r, i, xs) => xs.indexOf(r) === i).sort()
    : [...(app.reached_by || [])].sort();
  const toolNames = tools.filter((t) => t.app === app.name).map((t) => t.name);
  const names = toolNames.length ? toolNames : app.tools || [];
  const others = (me: Busy) => busy !== null && busy !== me;
  // Test a call runs policy simulate, the policy area, so the door is
  // offered only to a session whose standing reaches that area.
  const areas = adminAreas();
  const canTest = areas === null || !!areas.policy;
  // A session with the apps grant is the global admin of every server: it
  // reaches the roles of any server and may widen a role holders were
  // certified on, which the server's own admin may not.
  const globalAdmin = areas === null || !!areas.apps;
  // Removal stays with an apps grant, because it also deletes the office:
  // a server admin pauses their server and never removes it.
  const canRemove = globalAdmin;
  const ownedRoles = state.data.roles.filter((r) => r.server === app.name);
  const recheckButton = (primary: boolean) => (
    <Button variant={primary ? "default" : "outline"} onClick={recheck} disabled={others("recheck")} aria-busy={busy === "recheck" || undefined}>
      <RefreshCwIcon className={busy === "recheck" ? "animate-spin" : ""} /> Recheck
    </Button>
  );
  const actions = (
    <>
      {canRemove && (
        <Button variant="outline" className="border-danger text-danger hover:bg-danger-bg" onClick={() => setAsk("remove")} disabled={busy !== null || remover.busy !== null} aria-busy={remover.busy !== null || undefined}>
          {remover.busy !== null && <Loader2Icon className="animate-spin" />} Remove server
        </Button>
      )}
      {!stopped && (
        <Button variant="outline" onClick={() => setAsk("pause")} disabled={others("pause")} aria-busy={busy === "pause" || undefined}>
          {busy === "pause" && <Loader2Icon className="animate-spin" />} Pause
        </Button>
      )}
      {canTest && <Button variant="outline" onClick={() => setTestOpen(true)} disabled={busy !== null}>Test a call</Button>}
      {stopped ? (
        <>
          {recheckButton(false)}
          <Button onClick={enable} disabled={others("enable")} aria-busy={busy === "enable" || undefined}>
            {busy === "enable" && <Loader2Icon className="animate-spin" />} Enable
          </Button>
        </>
      ) : recheckButton(true)}
    </>
  );

  return (
    <>
      <div className="contents [&_h1]:font-mono">
        <PageHead label="MCP servers" title={app.name} description={app.manifest?.metadata?.description || NO_DESCRIPTION} actions={actions} />
      </div>
      <div className="flex flex-col gap-4 px-6 py-5">
        {state.problem && <FetchError subject="MCP server" detail={state.problem} lastRead={state.lastRead} />}
        <Banner app={app} reach={reach} last={last} reachReadable={state.data.reachReadable} />
        {remover.note && <SaveNoteLine note={remover.note} />}
        {problem && (problem.unreachable
          ? <FetchError subject={problem.subject} detail={problem.sentence} />
          : <RefusedError subject={problem.subject} message={cap(problem.sentence.slice(problem.subject.length + " refused: ".length))} />)}
        <Tabs value={current} onValueChange={(v) => navigate("servers", [id, v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="tools">Tools <span className="font-mono text-xs text-muted-foreground">{names.length}</span></TabsTrigger>
              <TabsTrigger value="roles">{TAB_ROLES} <span className="font-mono text-xs text-muted-foreground">{ownedRoles.length}</span></TabsTrigger>
              <TabsTrigger value="activity">Activity</TabsTrigger>
              <TabsTrigger value="manifest">Manifest</TabsTrigger>
            </TabsList>
          </div>
          <TabsContent value="overview"><ServerOverview app={app} upstreamTimeout={upstreamTimeout} onInspectAccess={() => navigate("servers", [id, "tools"], true)} onTest={canTest ? () => setTestOpen(true) : undefined} onChange={setSheet} onRefused={(err) => refuse("Secret", err)} /></TabsContent>
          <TabsContent value="tools"><ToolsTab app={app} tools={tools} bindings={bindings} reachReadable={state.data.reachReadable} globalAdmin={globalAdmin} onChange={!app.manifest ? null : () => setSheet("settings")} /></TabsContent>
          <TabsContent value="roles"><ServerRolesTab app={app} roles={ownedRoles} tools={tools} globalAdmin={globalAdmin} onChanged={() => void load()} /></TabsContent>
          <TabsContent value="activity"><ServerActivity app={app} /></TabsContent>
          <TabsContent value="manifest"><ManifestTab app={app} /></TabsContent>
        </Tabs>
      </div>

      <TestACall open={testOpen} onOpenChange={setTestOpen} app={{ id: app.id, name: app.name }} tools={names} />

      {sheet && (
        <React.Suspense fallback={null}>
          <ChangeSheet app={app} which={sheet} tools={names} upstreamTimeout={upstreamTimeout} onClose={() => setSheet(null)} onSaved={() => { setSheet(null); void load(); }} />
        </React.Suspense>
      )}

      <AlertDialog open={ask === "pause"} onOpenChange={(open) => { if (!open) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{"Pause " + app.name + "?"}</AlertDialogTitle>
            <AlertDialogDescription>An admin pause stops live traffic: its tools leave every session's list and every call to it is denied until you enable it again. Its process is stopped; a remote server is left running on its own host.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={pause}>Pause server</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={ask === "remove"} onOpenChange={(open) => { if (!open) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{"Remove " + app.name + "?"}</AlertDialogTitle>
            <AlertDialogDescription>
              {"The server leaves the list. "}
              {reach.length
                ? <>{"Its access rows for "}<b className="font-semibold text-foreground">{reach.join(", ")}</b>{" and its stored secrets are deleted, and sessions calling its tools find them gone."}</>
                : "No role has access to it; its stored secrets are deleted."}
              {" Rules naming it in policies stay and gate nothing until a server with this name is installed again."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button variant="outline" onClick={() => { setAsk(null); void remover.saveDraft(removal); }}>{SAVE_DRAFT}</Button>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={() => void remover.saveAndPublish(removal)}>{REMOVE_OPEN}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {remover.dialog}
    </>
  );
}
