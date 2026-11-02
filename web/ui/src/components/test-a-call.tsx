import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError, RefusedError } from "@/components/error-state";
import { type Gate, WhyCard, wireLine, type CardTone } from "@/components/why-card";
import { type ApiError, type AuditRow, type ObjectListing, type ObjectRow, type ObjectsBlock, type RoleRow, type SimObject, type SimulateAnswer, type SimulateRequest, appObjects, listAudit, listRoles, overview, query, simulate } from "@/lib/api";
import { cap, stripOf } from "@/lib/object-words";
import { version } from "@/lib/public";
import { readFailed } from "@/lib/say";
import { OUTCOME, SIMULATION_ONLY, unknownOutcome } from "@/lib/server-words";
import { aboutServer } from "@/lib/words";
import { cn } from "@/lib/utils";

// TestACall is the "what would happen" sheet for one MCP server, mounted
// from the server page header and from the wizard's Check step. It posts
// the pinned mcp.call event to the simulate endpoint, which tests the
// policies live and writes no audit record; a draft is never sent.
export type TestACallProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  app: { id: string; name: string };
  tools: string[];
};

const ATTESTATIONS = ["none", "advisory", "managed"];
// UNSET is the attestation item that sends nothing: a Radix item cannot
// carry an empty value, so the word stands in and the request omits it.
const UNSET = "unset";

const CAPS = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
const HINT = "text-[13px] leading-relaxed text-muted-foreground";
const PILL = "inline-flex h-8 items-center rounded-full border px-3 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50";
const PILL_ON = "border-link/40 bg-accent-bg text-foreground";
const PILL_OFF = "border-border bg-background text-text-2 hover:bg-accent";
const MISSING = "Pick at least one role, or name a user.";
const TOOL_MISSING = "Type the tool name to test.";

const EFFECT_HUE: Record<string, string> = { allow: "text-ok", deny: "text-danger", approve: "text-warn", confirm: "text-warn" };

// The words of the object a call names, on a server whose manifest pins an
// object type. Every noun is the server's type or plural, such as repo and
// repos. objectLine and refusedLine are strazactl policy simulate's
// sentences too (internal/ctl/policysim.go), so both tell one story. The
// Policies area's Test a call shares the picker and these words.
export type Pinned = { type: string; plural: string; tools: ObjectsBlock["tools"] };
const SEARCH_DEBOUNCE_MS = 300;
const SEARCH_LIMIT = 20;

// pinnedOf reads the nouns and the tool reaches of a pinned block.
export const pinnedOf = (b: ObjectsBlock): Pinned => ({ type: b.type, plural: b.plural || b.type + "s", tools: b.tools });
const objectHint = (p: Pinned, arg: string) => "Pick the " + p.type + " the call works on. Test a call sends it in the argument " + arg + ", as the agent would.";
export const objectMissing = (p: Pinned) => "Pick the " + p.type + " this call works on.";
const searchLabel = (p: Pinned) => "Search " + p.plural + " by name";

// emptyHint says why the picker offers nothing, with the next step: the
// search matched nothing, the listing needs its credential or has not run,
// or the list is empty and a refresh may fill it.
function emptyHint(app: string, p: Pinned, q: string, l?: ObjectListing): string {
  if (q) return "No " + p.type + " in the list matches " + q + ".";
  const strip = l ? stripOf(app, p, l) : null;
  if (strip && l && (strip.tone === "danger" || l.outcome !== "ok")) return strip.lines.join(" ");
  return "The " + p.type + " list is empty, so there is nothing to pick yet. If " + app + " has " + p.plural + " now, refresh the list with strazactl apps objects refresh " + app + ", then open Test a call again.";
}
const firstOnly = (p: Pinned) => "The first " + SEARCH_LIMIT + " " + p.plural + " are shown. Type to narrow the list.";

// objectLine names the held role that covers the call, and is empty when
// the object check refused it.
export function objectLine(o: SimObject): string {
  if (!o.covered) return "";
  if (!o.value) return o.role + " covers every " + o.type + ".";
  if (!o.id) return o.role + " covers every " + o.type + ", " + o.value + " included.";
  return o.role + " covers " + o.value + ", id " + o.id + ".";
}

// refusedLine opens a refusal of the object check: no policy ran.
export const refusedLine = (o: SimObject) => "The " + o.type + " check refused this call before any policy ran.";

// gateOf is the sentence a why card reads in place of the no-rule one when
// the object check refused the call, and undefined when it did not.
export const gateOf = (o?: SimObject): Gate | undefined => (o && o.refusal ? { lead: refusedLine(o), reason: o.refusal.replace(/^Straza: /, "") } : undefined);

// argumentOf answers the argument that names the object for a tool whose
// pinned reach is argument, and "" for every other tool.
export function argumentOf(p: Pinned | null, tool: string): string {
  const reach = p?.tools?.[tool];
  return reach && typeof reach === "object" ? reach.argument : "";
}

export function simulationOutcome(d: SimulateAnswer["active"]): { label: string; tone: CardTone } {
  if (d.effect === "deny") return { label: OUTCOME.deny, tone: "danger" };
  if (d.approve || (d as typeof d & { confirm?: boolean }).confirm || d.effect === "approve" || d.effect === "confirm") return { label: OUTCOME.approve, tone: "warn" };
  if (d.serverCheck || d.classify) return { label: OUTCOME.checks, tone: "warn" };
  return d.effect === "allow" ? { label: OUTCOME.allow, tone: "ok" } : { label: unknownOutcome(d.effect), tone: "plain" };
}

type RolesRead = { kind: "loading" } | { kind: "ready"; roles: RoleRow[] } | { kind: "failed"; detail: string };
type Replay = { seq: number; user: string; tool: string; effect: string; object?: ObjectRow };
type ReplayState = { kind: "closed" } | { kind: "loading" } | { kind: "ready"; rows: Replay[] } | { kind: "failed"; detail: string };
type Result = { answer: SimulateAnswer; subject: SimulateRequest["subject"] };

// orderRoles puts the application roles first, the only kind that carries
// access rows, and keeps the server's order inside each group.
function orderRoles(roles: RoleRow[]): RoleRow[] {
  return [...roles.filter((r) => r.kind === "application"), ...roles.filter((r) => r.kind !== "application")];
}

// replayRows keeps the gateway decisions about this server from an audit
// window, newest first, at most twenty.
function replayRows(rows: AuditRow[], name: string): Replay[] {
  const out: Replay[] = [];
  for (const r of rows || []) {
    let ce: { type?: string; data?: Record<string, unknown> } | null = null;
    try { ce = JSON.parse(r.ce); } catch { continue; }
    if (!ce || ce.type !== "straza.audit.mcp") continue;
    const d = ce.data || {};
    const about = aboutServer(d, name);
    if (!about.mine) continue;
    // A value the record cut is not the value the call named, so it fills
    // no pick.
    const o = d.object as { value?: string; id?: string; valueTruncated?: boolean } | undefined;
    out.push({ seq: r.seq, user: String(r.username || d.user || ""), tool: about.tool, effect: String(d.effect || ""), object: o && o.value && !o.valueTruncated ? { value: String(o.value), id: String(o.id || "") } : undefined });
  }
  return out.sort((a, b) => b.seq - a.seq).slice(0, 20);
}

// ResultCard is the decision on the shared why card: the effect word in its
// hue and the gates it carries as the head, then the subject and snapshot
// line. This sheet keeps the engine's own effect word, since it tests one
// server rather than reading a policy.
function ResultCard({ result, profile }: { result: Result; profile: string }) {
  const d = result.answer.active;
  const outcome = simulationOutcome(d);
  const who = result.subject.roles ? "roles " + result.subject.roles.join("/") : result.subject.user || "";
  const gate = "rounded-md border-link/40 bg-accent-bg font-mono text-[13px] font-normal text-foreground";
  const obj = result.answer.object;
  const subjectLine = <>{"subject: " + who + " · snapshot "}<span className="font-mono">{String(result.answer.snapshot || "").slice(0, 16)}</span>{SIMULATION_ONLY}</>;
  const covered = obj ? objectLine(obj) : "";
  return (
    <WhyCard
      d={d}
      profile={profile}
      mark="result"
      tone={outcome.tone}
      gate={gateOf(obj)}
      wire={wireLine(d, result.answer.snapshot || "", "live") + " serverCheck=" + !!d.serverCheck + " classify=" + !!d.classify}
      head={
        <>
          <span className="font-semibold">{outcome.label}</span>
          {d.serverCheck && <Badge variant="outline" className={gate}>{OUTCOME.serverCheck}</Badge>}
          {d.classify && <Badge variant="outline" className={gate}>{OUTCOME.classify}</Badge>}
        </>
      }
      context={
        <>
          {covered && <span className="block text-text-2" data-object-line>{covered}</span>}
          {subjectLine}
        </>
      }
    />
  );
}

type Search = { kind: "loading" } | { kind: "ready"; rows: ObjectRow[]; more: boolean; listing?: ObjectListing } | { kind: "failed"; detail: string };

// ObjectPicker picks the object a call names from the server's list, with
// the search on the server and no free text: a typed value is a search,
// never what is sent. The line under it previews the arguments the test
// sends.
export function ObjectPicker({ app, pinned, arg, picked, onPick }: { app: { id: string; name: string }; pinned: Pinned; arg: string; picked: ObjectRow | null; onPick: (o: ObjectRow | null) => void }) {
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");
  const [search, setSearch] = React.useState<Search>({ kind: "loading" });
  const seq = React.useRef(0);

  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  React.useEffect(() => {
    if (picked) return;
    const my = ++seq.current;
    setSearch({ kind: "loading" });
    appObjects(app.id, query({ q, limit: SEARCH_LIMIT })).then(
      (a) => { if (my === seq.current) setSearch({ kind: "ready", rows: a.objects || [], more: !!a.next, listing: a.listing }); },
      (e: ApiError) => { if (my === seq.current && e.status !== 401) setSearch({ kind: "failed", detail: readFailed("The " + pinned.plural, e) }); },
    );
  }, [app.id, q, picked, pinned.plural]);

  return (
    <div className="flex flex-col gap-1.5" data-object-picker>
      <span className={CAPS}>{pinned.type}</span>
      {picked ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className={cn(PILL, PILL_ON, "gap-2 font-mono")} data-picked>
            {picked.value}
            <button type="button" aria-label={"Clear the " + pinned.type} className="text-muted-foreground hover:text-foreground" onClick={() => onPick(null)}>×</button>
          </span>
          <span className="font-mono text-[13px] text-muted-foreground">{picked.id ? "id " + picked.id : ""}</span>
        </div>
      ) : (
        <>
          <Input value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={searchLabel(pinned)} placeholder={searchLabel(pinned)} className="h-9" spellCheck={false} />
          {search.kind === "loading" && <p className={HINT}>{"Reading the " + pinned.plural + "."}</p>}
          {search.kind === "failed" && <FetchError subject={cap(pinned.plural)} detail={search.detail} />}
          {search.kind === "ready" && search.rows.length === 0 && <p className={HINT} data-object-empty>{emptyHint(app.name, pinned, q, search.listing)}</p>}
          {search.kind === "ready" && search.rows.length > 0 && (
            <div className="max-h-48 overflow-y-auto rounded-md border border-border bg-card" data-object-list>
              {search.rows.map((o) => (
                <button key={o.id} type="button" onClick={() => onPick(o)}
                  className="flex w-full items-baseline gap-3 border-b border-border px-3 py-1.5 text-left text-[13px] last:border-b-0 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                  <span className="font-mono text-foreground">{o.value}</span>
                  {o.title && <span className="min-w-0 flex-1 truncate text-muted-foreground">{o.title}</span>}
                  <span className="ml-auto font-mono text-muted-foreground">{o.id}</span>
                </button>
              ))}
            </div>
          )}
          {search.kind === "ready" && search.more && <p className={HINT}>{firstOnly(pinned)}</p>}
        </>
      )}
      <p className={HINT}>{objectHint(pinned, arg)}</p>
      <p className="font-mono text-[13px] text-muted-foreground" data-args-preview>{"args " + JSON.stringify(picked ? { [arg]: picked.value } : {})}</p>
    </div>
  );
}

// Body is the form and its answer. It mounts fresh on every open, so the
// roles are read once per open and the last answer does not linger.
function Body({ app, tools }: { app: { id: string; name: string }; tools: string[] }) {
  const [rolesRead, setRolesRead] = React.useState<RolesRead>({ kind: "loading" });
  const [profile, setProfile] = React.useState("");
  const [mode, setMode] = React.useState<"roles" | "user">("roles");
  const [picked, setPicked] = React.useState<string[]>([]);
  const [freeRoles, setFreeRoles] = React.useState("");
  const [user, setUser] = React.useState("");
  const [attestation, setAttestation] = React.useState(UNSET);
  const [toolName, setToolName] = React.useState(tools[0] || "");
  const [replay, setReplay] = React.useState<ReplayState>({ kind: "closed" });
  const [running, setRunning] = React.useState(false);
  const [result, setResult] = React.useState<Result | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [unsupported, setUnsupported] = React.useState(false);
  const [missing, setMissing] = React.useState<string | null>(null);
  const [toolMissing, setToolMissing] = React.useState<string | null>(null);
  const [pinned, setPinned] = React.useState<Pinned | null>(null);
  const [target, setTarget] = React.useState<ObjectRow | null>(null);
  const [objectMissingNote, setObjectMissingNote] = React.useState<string | null>(null);
  const pillsRef = React.useRef<HTMLDivElement>(null);
  const freeRef = React.useRef<HTMLInputElement>(null);
  const userRef = React.useRef<HTMLInputElement>(null);
  const toolRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    let alive = true;
    listRoles().then(
      (rows) => {
        if (!alive) return;
        const roles = orderRoles(rows || []);
        setRolesRead({ kind: "ready", roles });
        const first = roles.find((r) => r.kind === "application");
        if (first) setPicked([first.name]);
      },
      (e: ApiError) => {
        if (!alive || e.status === 401) return;
        setRolesRead({
          kind: "failed",
          detail: e.unreachable
            ? "The roles could not be read because strazad did not answer. Type the role names instead, comma separated."
            : "The roles could not be read: " + e.message + ". Type the role names instead, comma separated.",
        });
      },
    );
    // The profile only words the no-rule refusal; a failed read leaves the
    // generic sentence, so nothing is shown for it.
    void version().then((v) => { if (alive) setProfile((v && v.profile) || ""); });
    // A server that pins an object type gets the picker on a tool that names
    // one. A failed read shows none, and the answer then says what the call
    // lacks, in the gateway's words.
    appObjects(app.id, query({ limit: 1 })).then(
      (a) => { if (alive && a && a.type && a.pinned) setPinned(pinnedOf(a.pinned)); },
      () => {},
    );
    return () => { alive = false; };
  }, []);

  // Any change to the question drops the old answer, so a result on screen
  // always belongs to the inputs beside it.
  const touch = () => { setResult(null); setProblem(null); setMissing(null); setToolMissing(null); setObjectMissingNote(null); };
  const pick = (o: ObjectRow | null) => { touch(); setTarget(o); };
  const arg = argumentOf(pinned, toolName.trim());

  const toggleRole = (name: string) => {
    touch();
    setPicked((p) => (p.includes(name) ? p.filter((r) => r !== name) : [...p, name]));
  };

  const run = async () => {
    const roles = rolesRead.kind === "failed" ? freeRoles.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean) : picked;
    const who = user.trim();
    const tool = toolName.trim();
    if (mode === "roles" ? roles.length === 0 : who === "") {
      setMissing(MISSING);
      if (mode === "user") userRef.current?.focus();
      else if (rolesRead.kind === "failed") freeRef.current?.focus();
      else pillsRef.current?.querySelector("button")?.focus();
      return;
    }
    if (tool === "") {
      setToolMissing(TOOL_MISSING);
      toolRef.current?.focus();
      return;
    }
    if (pinned && arg && !target) {
      setObjectMissingNote(objectMissing(pinned));
      document.querySelector<HTMLInputElement>("[data-object-picker] input")?.focus();
      return;
    }
    const subject: SimulateRequest["subject"] = mode === "roles" ? { roles } : { user: who };
    if (attestation !== UNSET) subject.attestation = attestation;
    const body: SimulateRequest = { event: { kind: "tool.pre", tool: "mcp.call", app: app.name, toolName: tool }, subject };
    if (arg && target) body.event.args = { [arg]: target.value };
    setRunning(true);
    setResult(null);
    setProblem(null);
    try {
      const answer = await simulate(body);
      setResult({ answer, subject });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      if (err.status === 404 && err.message === "HTTP 404") { setUnsupported(true); return; }
      setProblem(err.unreachable
        ? "The test did not reach strazad. Check the connection, then run it again."
        : "The server answered: " + err.message + ". Check the user name and the tool name, then run the test again.");
    } finally {
      setRunning(false);
    }
  };

  const loadReplay = async () => {
    setReplay({ kind: "loading" });
    try {
      const o = await overview();
      const head = (o && o.audit && o.audit.head_seq) || 0;
      const rows = await listAudit("after=" + Math.max(0, head - 200) + "&limit=1000");
      setReplay({ kind: "ready", rows: replayRows(rows, app.name) });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setReplay({
        kind: "failed",
        detail: err.unreachable
          ? "The recent calls could not be read because strazad did not answer. Try again, or fill the form by hand."
          : "The recent calls could not be read: " + err.message + ". Try again, or fill the form by hand.",
      });
    }
  };

  const applyReplay = (r: Replay) => {
    touch();
    setMode("user");
    setUser(r.user);
    if (r.tool) setToolName(r.tool);
    setTarget(r.object ?? null);
    setReplay({ kind: "closed" });
  };

  if (unsupported) {
    return (
      <div className="flex-1 px-4 pb-6">
        <p className="text-sm leading-relaxed text-text-2" data-unsupported>This server does not offer Test a call yet. Upgrade strazad to test calls from the console.</p>
      </div>
    );
  }

  // A replayed tool the server no longer lists still needs a row in the
  // picker, or the trigger would show nothing.
  const toolOptions = toolName && !tools.includes(toolName) ? [...tools, toolName] : tools;

  return (
    <>
      <div className="flex flex-1 flex-col gap-5 overflow-y-auto px-4 pb-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <span className={CAPS}>subject</span>
            <div className="flex gap-1">
              <button type="button" className={cn(PILL, mode === "roles" ? PILL_ON : PILL_OFF)} aria-pressed={mode === "roles"} onClick={() => { touch(); setMode("roles"); }}>roles</button>
              <button type="button" className={cn(PILL, mode === "user" ? PILL_ON : PILL_OFF)} aria-pressed={mode === "user"} onClick={() => { touch(); setMode("user"); }}>one user</button>
            </div>
            {mode === "roles" && rolesRead.kind === "loading" && <p className={HINT}>Reading the roles.</p>}
            {mode === "roles" && rolesRead.kind === "failed" && (
              <>
                <FetchError subject="Roles" detail={rolesRead.detail} />
                <Input ref={freeRef} value={freeRoles} onChange={(e) => { touch(); setFreeRoles(e.target.value); }} aria-label="roles" placeholder="role names, comma separated" className="h-9 font-mono" spellCheck={false} />
              </>
            )}
            {mode === "roles" && rolesRead.kind === "ready" && (
              <>
                <div ref={pillsRef} className="mt-0.5 flex flex-wrap gap-1" data-role-pills>
                  {rolesRead.roles.map((r) => (
                    <button key={r.id || r.name} type="button" className={cn(PILL, picked.includes(r.name) ? PILL_ON : PILL_OFF)} aria-pressed={picked.includes(r.name)} onClick={() => toggleRole(r.name)}>{r.name}</button>
                  ))}
                  {rolesRead.roles.length === 0 && <p className={HINT}>The server lists no role yet.</p>}
                </div>
                <p className={HINT}>Application roles carry access rows; the others are here so a wrong pick reads as a denial, not a surprise.</p>
              </>
            )}
            {mode === "user" && (
              <>
                <Input ref={userRef} value={user} onChange={(e) => { touch(); setUser(e.target.value); }} aria-label="user" placeholder="username, roles resolve on the server" className="h-9 font-mono" spellCheck={false} />
                <p className={HINT}>The server resolves the user's roles and typology, so the answer matches a live session.</p>
              </>
            )}
            {missing && <p className="text-[13px] text-danger" role="alert" data-missing>{missing}</p>}
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tac-attestation" className={CAPS}>attestation</Label>
            <Select value={attestation} onValueChange={(v) => { touch(); setAttestation(v); }}>
              <SelectTrigger id="tac-attestation" aria-label="attestation" className="h-9 w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={UNSET}>unset</SelectItem>
                {ATTESTATIONS.map((a) => <SelectItem key={a} value={a}>{a}</SelectItem>)}
              </SelectContent>
            </Select>
            <p className={HINT}>What the calling session proved about its hooks.</p>
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <span className={CAPS}>event</span>
            <span className="flex h-9 items-center font-mono text-sm text-text-2" data-event>{"tool.pre · mcp.call on " + app.name}</span>
            <p className={HINT}>Pinned to this server; the Policies page tests any event.</p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tac-tool" className={CAPS}>tool name</Label>
            {tools.length ? (
              <Select value={toolName} onValueChange={(v) => { touch(); setToolName(v); }}>
                <SelectTrigger id="tac-tool" aria-label="tool name" className="h-9 w-full font-mono"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {toolOptions.map((t) => <SelectItem key={t} value={t} className="font-mono">{t}</SelectItem>)}
                </SelectContent>
              </Select>
            ) : (
              <>
                <Input ref={toolRef} id="tac-tool" value={toolName} onChange={(e) => { touch(); setToolName(e.target.value); }} aria-label="tool name" placeholder="tool name" className="h-9 font-mono" spellCheck={false} />
                <p className={HINT}>The server listed no tool yet; type the name to test.</p>
              </>
            )}
            {toolMissing && <p className="text-[13px] text-danger" role="alert">{toolMissing}</p>}
          </div>
        </div>

        {pinned && arg && (
          <div className="flex flex-col gap-1.5">
            <ObjectPicker app={app} pinned={pinned} arg={arg} picked={target} onPick={pick} />
            {objectMissingNote && <p className="text-[13px] text-danger" role="alert" data-object-missing>{objectMissingNote}</p>}
          </div>
        )}

        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => (replay.kind === "closed" ? void loadReplay() : setReplay({ kind: "closed" }))}>
              {replay.kind === "closed" ? "Replay a recent call to this server" : "Close the replay list"}
            </Button>
            <span className={HINT}>Prefills the form from a real record.</span>
          </div>
          {replay.kind === "loading" && <p className={HINT}>Reading the recent calls.</p>}
          {replay.kind === "failed" && <FetchError subject="Recent calls" detail={replay.detail} />}
          {replay.kind === "ready" && replay.rows.length === 0 && <p className={HINT} data-replay-empty>No call to this server in the recent audit window.</p>}
          {replay.kind === "ready" && replay.rows.length > 0 && (
            <div className="max-h-48 overflow-y-auto rounded-md border border-border bg-card" data-replay-list>
              {replay.rows.map((r) => (
                <button
                  key={r.seq}
                  type="button"
                  onClick={() => applyReplay(r)}
                  className="flex w-full items-center gap-3 border-b border-border px-3 py-1.5 text-left font-mono text-[13px] text-foreground last:border-b-0 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                >
                  <span className="text-muted-foreground">{"#" + r.seq}</span>
                  <span>{r.user}</span>
                  <span className="min-w-0 flex-1 truncate">{r.tool}</span>
                  <span className={cn("font-semibold uppercase", EFFECT_HUE[r.effect] || "text-muted-foreground")}>{r.effect}</span>
                </button>
              ))}
            </div>
          )}
        </div>

        {problem && <RefusedError subject="Test a call" message={problem} />}
        {result && <ResultCard result={result} profile={profile} />}
      </div>

      <div className="flex items-center gap-3 border-t border-border px-4 py-4">
        <span className={cn(HINT, "min-w-0 flex-1")}>Test a call runs the access check first, then the engine. It is the door for an operator who has no agent yet.</span>
        <Button onClick={() => void run()} disabled={running} className="shrink-0" data-run>
          {running && <Loader2Icon className="animate-spin" />}
          {running ? "Running" : "Run test"}
        </Button>
      </div>
    </>
  );
}

export function TestACall({ open, onOpenChange, app, tools }: TestACallProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl" data-test-a-call>
        <SheetHeader className="pb-0">
          <SheetTitle className="text-lg font-semibold">Test a call: what would happen</SheetTitle>
          <SheetDescription>{"Testing the policies live right now, on " + app.name + ". Nothing is saved or enforced, and no audit record is written."}</SheetDescription>
        </SheetHeader>
        <Body app={app} tools={tools} />
      </SheetContent>
    </Sheet>
  );
}
