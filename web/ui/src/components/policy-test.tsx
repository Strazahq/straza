import * as React from "react";
import { Loader2Icon, PlayIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError, RefusedError } from "@/components/error-state";
import { Section } from "@/components/sheet-parts";
import { type PickedUser, UserPicker } from "@/components/user-picker";
import { Outcome, WhyCard, decidersLine, wireLine } from "@/components/why-card";
import { type ApiError, type AppRow, type BindingRow, type Decision, type SimulateAnswer, type SimulateRequest, type ToolRow, type UserRow, listApps, listBindings, listTools, listUsers, query, simulate } from "@/lib/api";
import { version } from "@/lib/public";
import {
  CLOSE, DECIDED_LIVE, LANE_LABEL, LIVE_NOW, NET_LINE, SERVER, SUBJECT_SERVERS, TEST, TEST_ANSWER, TEST_COMMAND,
  TEST_EVERY_SERVER, TEST_FROM_AUDIT, TEST_LEDE, TEST_LEDE_DRAFT, TEST_MISSING_WHAT, TEST_MISSING_WHO, TEST_NO_SERVERS,
  TEST_PATH, TEST_PATH_LINE, TEST_SERVERS_HINT, TEST_TITLE, TEST_TOOL, TEST_WHAT, TEST_WHO, TEST_WHO_HINT, WITH_EDITS,
  WITH_EDITS_LINE, holdsLine,
} from "@/lib/policy-words";
import { checkFailed, readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// PolicyTest is the Test a call sheet of the Policies area: who calls, what
// they call on which lane, and the answer as the
// why card, twice when the page carries unpublished edits. It writes
// nothing: simulate runs the live engine and records no audit event.

export type PolicyTestPrefill = {
  user?: string;
  lane?: "mcp" | "shell" | "files" | "net";
  app?: string;
  tool?: string;
  command?: string;
  path?: string;
};

export type PolicyTestProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // draft is the page's unpublished text; the sheet then answers live now
  // and with the edits.
  draft?: { name: string; yaml: string } | null;
  prefill?: PolicyTestPrefill;
};

type Lane = "mcp" | "shell" | "files" | "net";

const LANES: Lane[] = ["mcp", "shell", "files", "net"];

// TOOL_OF is the event tool each lane tests. The file lane tests a write,
// the strictest of the file verbs.
const TOOL_OF: Record<Lane, string> = { mcp: "mcp.call", shell: "shell.exec", files: "file.write", net: "net.fetch" };

const CAPS = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
const HINT = "text-[13px] leading-relaxed text-muted-foreground";
const ERROR = "text-[13px] text-danger";
const PILL = "inline-flex h-8 items-center rounded-full border px-3 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50";
const PILL_ON = "border-link/40 bg-accent-bg text-foreground";
const PILL_OFF = "border-border bg-background text-text-2 hover:bg-accent";

// Catalog is what the What section picks from: the servers, their tools,
// and the access rows that say which server a role reaches.
type Catalog = { apps: AppRow[]; tools: ToolRow[]; bindings: BindingRow[] };
type Read = { kind: "loading" } | { kind: "ready"; catalog: Catalog } | { kind: "failed"; detail: string };

// Ask is one filled-in question, passed whole so a prefilled open can test
// before its state updates land.
type Ask = { user: string; lane: Lane; app: string; toolName: string; command: string; path: string };

function eventOf(ask: Ask): SimulateRequest["event"] {
  const event: SimulateRequest["event"] = { kind: "tool.pre", tool: TOOL_OF[ask.lane] };
  if (ask.lane === "mcp") {
    event.app = ask.app;
    event.toolName = ask.toolName;
  }
  if (ask.lane === "shell") event.command = ask.command;
  if (ask.lane === "files") event.paths = [ask.path];
  return event;
}

// contextLine is the sentence under the outcome: who decides when the call
// waits for a person, then which version of the policies answered.
const contextLine = (d: Decision, which: string) => [decidersLine(d), which].filter(Boolean).join(" ");

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name);

// withCurrent keeps a prefilled name in its picker even when the catalog
// does not list it, so the trigger never reads empty for a call that was
// really made.
const withCurrent = (names: string[], current: string) => (current && !names.includes(current) ? [...names, current] : names);

// Body is the form and its answer. It mounts on every open, so the reads
// run once per open and the last answer does not linger.
function Body({ draft, prefill, onClose }: { draft?: { name: string; yaml: string } | null; prefill?: PolicyTestPrefill; onClose: () => void }) {
  const [read, setRead] = React.useState<Read>({ kind: "loading" });
  const [profile, setProfile] = React.useState("");
  const [who, setWho] = React.useState<PickedUser | null>(prefill?.user ? { id: "", username: prefill.user } : null);
  const [row, setRow] = React.useState<UserRow | null>(null);
  const [lane, setLane] = React.useState<Lane>(prefill?.lane || "mcp");
  const [app, setApp] = React.useState(prefill?.app || "");
  const [toolName, setToolName] = React.useState(prefill?.tool || "");
  const [command, setCommand] = React.useState(prefill?.command || "");
  const [path, setPath] = React.useState(prefill?.path || "");
  const [running, setRunning] = React.useState(false);
  const [answer, setAnswer] = React.useState<SimulateAnswer | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [missWho, setMissWho] = React.useState(false);
  const [missWhat, setMissWhat] = React.useState(false);

  // fire asks the server, and keeps the answer on screen when the next ask
  // is refused, so a refusal never empties the sheet.
  const fire = async (ask: Ask) => {
    const body: SimulateRequest = { event: eventOf(ask), subject: { user: ask.user } };
    if (draft) body.draft = draft.yaml;
    setRunning(true);
    setRefusal(null);
    try {
      setAnswer(await simulate(body));
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setRefusal(checkFailed(err));
    } finally {
      setRunning(false);
    }
  };

  React.useEffect(() => {
    let alive = true;
    Promise.all([listApps(), listTools(), listBindings()]).then(
      ([apps, tools, bindings]) => {
        if (alive) setRead({ kind: "ready", catalog: { apps: [...(apps || [])].sort(byName), tools: tools || [], bindings: bindings || [] } });
      },
      (e: ApiError) => {
        if (alive && e.status !== 401) setRead({ kind: "failed", detail: readFailed(SUBJECT_SERVERS, e) });
      },
    );
    // The profile only words the no-rule sentence; a failed read leaves the
    // generic one, so nothing is shown for it.
    void version().then((v) => { if (alive) setProfile((v && v.profile) || ""); });
    return () => { alive = false; };
  }, []);

  // A prefilled sheet comes from a record that was really decided, so it
  // resolves the person and answers at once.
  React.useEffect(() => {
    const named = prefill?.user;
    if (!named) return;
    let alive = true;
    void resolve(named).then((found) => {
      if (!alive) return;
      if (found) {
        setRow(found);
        setWho({ id: found.id, username: found.username });
      }
      void fire({
        user: found?.id || named,
        lane: prefill?.lane || "mcp",
        app: prefill?.app || "",
        toolName: prefill?.tool || "",
        command: prefill?.command || "",
        path: prefill?.path || "",
      });
    });
    return () => { alive = false; };
  }, []);

  // Any change to the question drops the old answer, so a card on screen
  // always belongs to the call beside it.
  const touch = () => {
    setAnswer(null);
    setRefusal(null);
    setMissWho(false);
    setMissWhat(false);
  };

  const pick = (picked: PickedUser | null) => {
    touch();
    setWho(picked);
    setRow(null);
    if (picked) void resolve(picked.username).then((found) => { if (found) setRow(found); });
  };

  const test = () => {
    const user = row?.id || who?.username || "";
    const what = lane === "mcp" ? toolName : lane === "shell" ? command.trim() : lane === "files" ? path.trim() : TOOL_OF.net;
    setMissWho(!user);
    setMissWhat(!what);
    if (!user || !what) return;
    void fire({ user, lane, app, toolName, command: command.trim(), path: path.trim() });
  };

  const catalog = read.kind === "ready" ? read.catalog : null;
  // reached narrows the servers to the ones a role this person holds can
  // call; without a resolved person every server is offered.
  const reached = React.useMemo(() => {
    if (!catalog || !row || !row.effective_roles || row.effective_roles.length === 0) return null;
    const held = new Set(row.effective_roles);
    const names = new Set(catalog.bindings.filter((b) => held.has(b.role)).map((b) => b.app));
    return catalog.apps.filter((a) => names.has(a.name));
  }, [catalog, row]);

  const servers = withCurrent((reached || catalog?.apps || []).map((a) => a.name), app);
  const tools = withCurrent((catalog?.tools || []).filter((t) => t.app === app).map((t) => t.name).sort(), toolName);
  const roles = (answer && answer.subject.roles) || [];

  return (
    <>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
        <Section title={TEST_WHO}>
          <UserPicker value={who} onChange={pick} label={TEST_WHO} className="max-w-xs" />
          <p className={HINT}>{TEST_WHO_HINT}</p>
          {who && roles.length > 0 && <p className={HINT} data-holds>{holdsLine(who.username, roles)}</p>}
          {missWho && <p className={ERROR} role="alert">{TEST_MISSING_WHO}</p>}
        </Section>

        <Section title={TEST_WHAT}>
          <div className="flex flex-wrap gap-1" data-lanes>
            {LANES.map((l) => (
              <button
                key={l}
                type="button"
                aria-pressed={lane === l}
                className={cn(PILL, lane === l ? PILL_ON : PILL_OFF)}
                onClick={() => { touch(); setLane(l); }}
              >
                {LANE_LABEL[l]}
              </button>
            ))}
          </div>

          {read.kind === "failed" && lane === "mcp" && <FetchError subject={SUBJECT_SERVERS} detail={read.detail} />}

          {lane === "mcp" && (
            <div className="flex flex-col gap-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Select value={app} onValueChange={(v) => { touch(); setApp(v); setToolName(""); }}>
                  <SelectTrigger aria-label={SERVER} className="h-9 w-[220px]"><SelectValue placeholder={SERVER} /></SelectTrigger>
                  <SelectContent>
                    {servers.map((s) => <SelectItem key={s} value={s} className="font-mono">{s}</SelectItem>)}
                  </SelectContent>
                </Select>
                <Select value={toolName} onValueChange={(v) => { touch(); setToolName(v); }} disabled={!app}>
                  <SelectTrigger aria-label={TEST_TOOL} className="h-9 w-[220px]"><SelectValue placeholder={TEST_TOOL} /></SelectTrigger>
                  <SelectContent>
                    {tools.map((t) => <SelectItem key={t} value={t} className="font-mono">{t}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
              {reached && reached.length === 0
                ? <p className={HINT} data-no-servers>{TEST_NO_SERVERS}</p>
                : <p className={HINT}>{reached ? TEST_SERVERS_HINT : TEST_EVERY_SERVER}</p>}
            </div>
          )}

          {lane === "shell" && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="pt-command" className={CAPS}>{TEST_COMMAND}</Label>
              <Input id="pt-command" value={command} onChange={(e) => { touch(); setCommand(e.target.value); }} className="h-9 max-w-md font-mono" spellCheck={false} />
            </div>
          )}

          {lane === "files" && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="pt-path" className={CAPS}>{TEST_PATH}</Label>
              <Input id="pt-path" value={path} onChange={(e) => { touch(); setPath(e.target.value); }} className="h-9 max-w-md font-mono" spellCheck={false} />
              <p className={HINT}>{TEST_PATH_LINE}</p>
            </div>
          )}

          {lane === "net" && <p className={HINT}>{NET_LINE}</p>}

          {missWhat && <p className={ERROR} role="alert">{TEST_MISSING_WHAT}</p>}
        </Section>

        {(answer || refusal) && (
          <Section title={TEST_ANSWER}>
            {refusal && <RefusedError subject={TEST_TITLE} message={refusal} />}
            {answer && (
              <WhyCard
                d={answer.active}
                profile={profile}
                mark="live"
                caption={answer.draft ? LIVE_NOW : undefined}
                head={<Outcome d={answer.active} />}
                context={contextLine(answer.active, DECIDED_LIVE)}
                wire={wireLine(answer.active, answer.snapshot, "live")}
              />
            )}
            {answer && answer.draft && (
              <WhyCard
                d={answer.draft}
                profile={profile}
                mark="draft"
                caption={WITH_EDITS}
                head={<Outcome d={answer.draft} />}
                context={contextLine(answer.draft, WITH_EDITS_LINE)}
                wire={wireLine(answer.draft, answer.snapshot, "draft")}
              />
            )}
          </Section>
        )}
      </div>

      <SheetFooter className="mt-0 flex-row items-center gap-3 border-t border-border">
        <span className={cn(HINT, "min-w-0 flex-1")}>{TEST_FROM_AUDIT}</span>
        <Button variant="outline" onClick={onClose}>{CLOSE}</Button>
        <Button onClick={test} disabled={running} data-test-run>
          {running ? <Loader2Icon className="animate-spin" /> : <PlayIcon />} {TEST}
        </Button>
      </SheetFooter>
    </>
  );
}

// resolve reads the picked person's row, the source of the roles that
// narrow the servers and of the id the request sends. A row that does not
// come back leaves the name for the server to resolve.
async function resolve(username: string): Promise<UserRow | null> {
  try {
    const page = await listUsers(query({ q: username, limit: 8 }));
    return (page.items || []).find((u) => u.username === username) || null;
  } catch {
    return null;
  }
}

export function PolicyTest({ open, onOpenChange, draft, prefill }: PolicyTestProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-xl" data-policy-test>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg font-semibold">{TEST_TITLE}</SheetTitle>
          <SheetDescription>{draft ? TEST_LEDE_DRAFT : TEST_LEDE}</SheetDescription>
        </SheetHeader>
        <Body draft={draft} prefill={prefill} onClose={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  );
}
