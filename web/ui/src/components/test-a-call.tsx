import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError, RefusedError } from "@/components/error-state";
import { WhyCard, wireLine, type CardTone } from "@/components/why-card";
import { type ApiError, type AuditRow, type RoleRow, type SimulateAnswer, type SimulateRequest, listAudit, listRoles, overview, simulate } from "@/lib/api";
import { version } from "@/lib/public";
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

export function simulationOutcome(d: SimulateAnswer["active"]): { label: string; tone: CardTone } {
  if (d.effect === "deny") return { label: OUTCOME.deny, tone: "danger" };
  if (d.approve || (d as typeof d & { confirm?: boolean }).confirm || d.effect === "approve" || d.effect === "confirm") return { label: OUTCOME.approve, tone: "warn" };
  if (d.serverCheck || d.classify) return { label: OUTCOME.checks, tone: "warn" };
  return d.effect === "allow" ? { label: OUTCOME.allow, tone: "ok" } : { label: unknownOutcome(d.effect), tone: "plain" };
}

type RolesRead = { kind: "loading" } | { kind: "ready"; roles: RoleRow[] } | { kind: "failed"; detail: string };
type Replay = { seq: number; user: string; tool: string; effect: string };
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
    out.push({ seq: r.seq, user: String(r.username || d.user || ""), tool: about.tool, effect: String(d.effect || "") });
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
  return (
    <WhyCard
      d={d}
      profile={profile}
      mark="result"
      tone={outcome.tone}
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
          {"subject: " + who + " · snapshot "}<span className="font-mono">{String(result.answer.snapshot || "").slice(0, 16)}</span>
          {SIMULATION_ONLY}
        </>
      }
    />
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
    return () => { alive = false; };
  }, []);

  // Any change to the question drops the old answer, so a result on screen
  // always belongs to the inputs beside it.
  const touch = () => { setResult(null); setProblem(null); setMissing(null); setToolMissing(null); };

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
    const subject: SimulateRequest["subject"] = mode === "roles" ? { roles } : { user: who };
    if (attestation !== UNSET) subject.attestation = attestation;
    const body: SimulateRequest = { event: { kind: "tool.pre", tool: "mcp.call", app: app.name, toolName: tool }, subject };
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
