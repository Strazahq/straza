import * as React from "react";
import { Loader2Icon, PlayIcon } from "lucide-react";
import { PolicyTest } from "@/components/policy-test";
import { SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import {
  type Decision, type PolicySetRow, type SimulateRequest, type UserRow,
  listPolicies, listSessions, listUsers, query, simulate,
} from "@/lib/api";
import { type Bucket, type Postures, NO_POSTURES, addPostures, openDoc, posturesOf, posturesOfRules, rulesOf } from "@/lib/policy-model";
import { CANCEL, EVERYONE, PROBE_ORIGIN, PUBLISH, PUBLISH_DRAFT_NOTE, STRIP, TEST_FIRST, VERDICT, blastSentence, list, publishTitle, rulesStrip, savedToast, verdictBy, whatChanges } from "@/lib/policy-words";
import { cn } from "@/lib/utils";

// The publish dialog of the Policies area: what the
// document changes, what one call decides before and after it, the rule
// counts for the roles it names, and how many live sessions re-evaluate.
// Every read fails soft into its own sentence, so a dark corner never
// blocks the publish. Publish runs the editors' Save and publish with one
// PolicySet item, so a change that loosens a gate asks for its tick in the
// drafts' own dialog.

export type PublishProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  name: string;
  // text is the document to store.
  text: string;
  // baseText is the stored text it replaces, null for a new policy.
  baseText: string | null;
  // wasLive says the stored version runs, so the publish activates again.
  wasLive: boolean;
  // turnOn says the policy is off and the publish should turn it on, the
  // choice the page's Save and publish makes for a policy that gates
  // nothing yet.
  turnOn?: boolean;
  // roles are the roles the document matches, empty for everyone.
  roles: string[];
  // probe is the call the strip reads before and after.
  probe?: { event: SimulateRequest["event"]; user?: string; label: string };
  onDone: (result: { name: string; snapshot?: string }) => void;
};

// PAGE is how many rows the blast reads before it says the count is a
// floor.
const PAGE = 500;

// Blast is what the sessions row says: the active sessions, how many hold
// the roles, and which reads were dark.
type Blast = { total: number; covered: number; names: string[]; extra: number; capped: boolean; unknown: boolean; holdersUnknown: boolean; roles: string[] };

// NAMED is how many holders the sentence names before it counts the rest.
const NAMED = 2;

// changeOf reads the rule diff by id: how many rules are added, which ones
// differ, and which ones are gone.
export function changeOf(baseText: string | null, text: string): { added: number; changed: string[]; removed: string[] } {
  const read = (yaml: string) => {
    const doc = openDoc(yaml);
    return new Map(rulesOf(doc).map((r) => [r.id, JSON.stringify(r.raw)]));
  };
  const base = baseText === null ? new Map<string, string>() : read(baseText);
  const next = read(text);
  let added = 0;
  const changed: string[] = [];
  for (const [id, body] of next) {
    if (!base.has(id)) { added++; continue; }
    if (base.get(id) !== body) changed.push(id);
  }
  const removed = [...base.keys()].filter((id) => !next.has(id));
  return { added, changed, removed };
}

// bucketOfDecision folds one decision into the word the strip speaks.
function bucketOfDecision(d: Decision): Bucket {
  if (d.effect === "deny") return "deny";
  if (d.effect === "approve" || d.effect === "confirm" || d.approve || d.serverCheck || d.classify) return "hum";
  return "allow";
}

// verdictWords says what the call does, and names the policy that decided
// it when another one did.
function verdictWords(d: Decision | undefined, name: string): string {
  if (!d) return "";
  const word = VERDICT[bucketOfDecision(d)];
  return verdictBy(word, d.setName && d.setName !== name ? d.setName : null);
}

// posturesFor sums the live sets that govern the same sessions this
// document does: the ones naming any of its roles, or the ones naming no
// role when it applies to everyone.
function posturesFor(rows: PolicySetRow[], roles: string[], skip: string): Postures {
  let out = NO_POSTURES;
  for (const row of rows) {
    if (row.status !== "active" || row.name === skip) continue;
    const matched = (row.summary && row.summary.matchRoles) || [];
    const mine = roles.length ? roles.some((r) => matched.includes(r)) : matched.length === 0;
    if (mine) out = addPostures(out, posturesOf(row.summary && row.summary.postures));
  }
  return out;
}

// blastOf joins the active sessions with the people who hold the roles.
async function blastOf(roles: string[]): Promise<Blast> {
  const out: Blast = { total: 0, covered: 0, names: [], extra: 0, capped: false, unknown: false, holdersUnknown: false, roles };
  let sessions;
  try {
    sessions = await listSessions(query({ status: "active", limit: PAGE }));
  } catch {
    out.unknown = true;
    return out;
  }
  const rows = sessions.items || [];
  out.total = rows.length;
  out.capped = !!sessions.next_cursor;
  if (!roles.length) return out;

  const answers = await Promise.allSettled(roles.map((role) => listUsers(query({ role, limit: PAGE }))));
  const holders = new Set<string>();
  for (const answer of answers) {
    if (answer.status === "rejected") { out.holdersUnknown = true; continue; }
    if (answer.value.next_cursor) out.capped = true;
    for (const u of (answer.value.items || []) as UserRow[]) {
      holders.add(u.id);
      if (u.username) holders.add(u.username);
    }
  }
  if (out.holdersUnknown) return out;

  const covered = rows.filter((s) => holders.has(s.user_id) || (s.username ? holders.has(s.username) : false));
  out.covered = covered.length;
  const people = [...new Set(covered.map((s) => s.username || s.user_id))];
  out.names = people.slice(0, NAMED);
  out.extra = people.length - out.names.length;
  return out;
}

// Body is the dialog's content. It mounts on every open, so the strip is
// read fresh each time and no answer lingers from the last one.
function Body({ name, text, baseText, wasLive, turnOn, roles, probe, onOpenChange, onDone }: Omit<PublishProps, "open">) {
  const [answer, setAnswer] = React.useState<{ now?: Decision; after?: Decision } | null>(null);
  const [rows, setRows] = React.useState<PolicySetRow[] | null>(null);
  const [blast, setBlast] = React.useState<Blast | null>(null);
  const [testing, setTesting] = React.useState(false);
  const isNew = baseText === null;
  // on says the publish makes the text the version that runs: the set runs,
  // is new, or is to be turned on. Otherwise the set keeps its new text and
  // stays off, and the toast says so.
  const on = wasLive || isNew || !!turnOn;
  const save = useDraftSave({ name, toast: on ? undefined : savedToast(name, false), onPublished: (done) => onDone({ name, snapshot: done.snapshot }) });
  const busy = save.busy !== null;

  const diff = React.useMemo(() => changeOf(baseText, text), [baseText, text]);
  const mine = React.useMemo(() => posturesOfRules(rulesOf(openDoc(text))), [text]);

  React.useEffect(() => {
    let alive = true;
    if (probe) {
      simulate({ event: probe.event, subject: { user: probe.user }, draft: text }).then(
        (a) => { if (alive) setAnswer({ now: a.active, after: a.draft }); },
        () => { if (alive) setAnswer({}); },
      );
    }
    listPolicies(query({ limit: 0 })).then((a) => { if (alive) setRows(a.items || []); }, () => { if (alive) setRows([]); });
    void blastOf(roles).then((b) => { if (alive) setBlast(b); });
    return () => { alive = false; };
    // The dialog reads once per open; its inputs cannot change while it is
    // on screen.
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // publish sends the text as the set's one item, on or kept off.
  const publish = () => void save.saveAndPublish([{ kind: "PolicySet", name, op: on ? "put" : "off", doc: text }]);

  // now counts the live sets beside this one, plus the version of this set
  // that runs when the publish replaces one, so the before column is what
  // is live and not what is stored.
  const base = baseText !== null && wasLive ? posturesOfRules(rulesOf(openDoc(baseText))) : NO_POSTURES;
  const now = rows ? addPostures(posturesFor(rows, roles, name), base) : null;
  const after = rows ? addPostures(posturesFor(rows, roles, name), mine) : null;
  const nowParts = now ? rulesStrip(now).split(" · ") : [];
  const afterParts = after ? rulesStrip(after).split(" · ") : [];
  const who = roles.length ? list(roles) : EVERYONE;

  return (
    <>
      <DialogHeader>
        <DialogTitle>{publishTitle(name)}</DialogTitle>
        <DialogDescription>{whatChanges(diff.added, diff.changed, diff.removed, roles, isNew, wasLive)}</DialogDescription>
        {turnOn && <p className="text-sm leading-relaxed text-text-2" data-turn-on-note>{PUBLISH_DRAFT_NOTE}</p>}
      </DialogHeader>

      <div className="overflow-hidden rounded-md border border-border" data-publish-strip>
        <Table>
          <TableBody>
            {probe && (
              <TableRow className="hover:bg-transparent">
                <TableCell className="w-40 align-top text-[13px] text-muted-foreground">{STRIP.call}</TableCell>
                <TableCell colSpan={3} className="whitespace-normal text-sm text-foreground">
                  {probe.label} <span className="text-muted-foreground">{"· " + PROBE_ORIGIN}</span>
                </TableCell>
              </TableRow>
            )}
            {probe && (
              <TableRow className="hover:bg-transparent">
                <TableCell className="w-40 align-top text-[13px] text-muted-foreground">{STRIP.now}</TableCell>
                <TableCell className="whitespace-normal text-sm text-text-2" data-verdict-now>{verdictWords(answer?.now, name)}</TableCell>
                <TableCell className="w-8 text-center text-muted-foreground" aria-hidden="true">→</TableCell>
                <TableCell className="whitespace-normal text-sm text-foreground" data-verdict-after>{verdictWords(answer?.after, name)}</TableCell>
              </TableRow>
            )}
            <TableRow className="hover:bg-transparent">
              <TableCell className="w-40 align-top text-[13px] text-muted-foreground">{STRIP.rules + " " + who}</TableCell>
              <TableCell className="whitespace-normal text-sm text-muted-foreground" data-rules-now>{nowParts.join(" · ")}</TableCell>
              <TableCell className="w-8 text-center text-muted-foreground" aria-hidden="true">→</TableCell>
              <TableCell className="whitespace-normal text-sm text-text-2" data-rules-after>
                {afterParts.map((part, i) => (
                  <span key={part} className={cn(part !== nowParts[i] && "font-semibold text-foreground")}>{i ? " · " : ""}{part}</span>
                ))}
              </TableCell>
            </TableRow>
            <TableRow className="hover:bg-transparent">
              <TableCell className="w-40 align-top text-[13px] text-muted-foreground">{STRIP.sessions}</TableCell>
              <TableCell colSpan={3} className="whitespace-normal text-sm text-text-2" data-blast>{blast ? blastSentence(blast) : ""}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>

      {save.note && <SaveNoteLine note={save.note} />}

      <DialogFooter className="sm:justify-between">
        <Button variant="ghost" onClick={() => setTesting(true)}><PlayIcon /> {TEST_FIRST}</Button>
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>{CANCEL}</Button>
          <Button onClick={publish} disabled={busy} data-publish>{busy && <Loader2Icon className="animate-spin" />}{PUBLISH}</Button>
        </div>
      </DialogFooter>
      {save.dialog}

      <PolicyTest
        open={testing}
        onOpenChange={setTesting}
        draft={{ name, yaml: text }}
        prefill={probe ? { user: probe.user, lane: laneOfEvent(probe.event), app: probe.event.app, tool: probe.event.toolName, command: probe.event.command, path: (probe.event.paths || [])[0] } : undefined}
      />
    </>
  );
}

// laneOfEvent reads the lane back out of the probe event, so Test a call
// opens on the same lane the wizard asked about.
function laneOfEvent(event: SimulateRequest["event"]): "mcp" | "shell" | "files" | "net" {
  if (event.tool === "shell.exec") return "shell";
  if (event.tool === "net.fetch") return "net";
  if (event.tool.startsWith("file.")) return "files";
  return "mcp";
}

export function PolicyPublish({ open, ...rest }: PublishProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent className="sm:max-w-[720px]" data-publish-dialog>
        <Body {...rest} />
      </DialogContent>
    </Dialog>
  );
}
