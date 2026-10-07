import { HEAD } from "@/components/data-table";
import { HelpTip } from "@/components/help-tip";
import { RecChip } from "@/components/policies-table";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { BindingRow, PolicySetRow, RoleRow } from "@/lib/api";
import { type Bucket, type Postures, NO_POSTURES, addPostures, posturesOf } from "@/lib/policy-model";
import { CATEGORY, DOOR_APPROVE, DOOR_APPROVE_EVERYONE_TITLE, DOOR_DENY, DOOR_DENY_EVERYONE_TITLE, EVERYONE, EVERYONE_LINE, FLOOR_LINE, NO_ALLOW_EVERYONE, NO_ALLOW_ROLE, NO_RECORDING, NO_ROLE_POLICY, ROLE_COLUMN, ROLE_FOOT, ROLE_HELP, allowWords, denyWords, doorApproveTitle, doorDenyTitle, draftLine, humSplit, liveLine, reachLine, recWord, setChipTitle } from "@/lib/policy-words";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { cn } from "@/lib/utils";

// The By role view of the Policies list: one row per
// application role, plus Everyone above them and Outside roles below when
// identity-scoped policies exist. It answers what governs a role today, so
// the counts sum the role's own live policies and an empty cell is a door
// into New policy with the role already picked.

// PolicyDoor is what a door hands the New policy wizard: the role it opens
// on (null for Everyone, absent for no choice), the intent the door names,
// and the lane when the Lane filter names one.
export type PolicyDoor = { role?: string | null; intent?: "deny" | "approve"; lane?: string };

// openNewPolicy hands the wizard the prefill and goes there. The prefill
// travels in module memory, never the address, so a reload lands on the
// plain wizard.
export function openNewPolicy(door: PolicyDoor) {
  put("policies-new", door);
  navigate("policies", ["new"]);
}

const WIDTHS: Record<string, string> = { role: "22%", policies: "28%", deny: "9%", hum: "14%", allow: "9%", recording: "18%" };

const TONE: Record<Bucket, string> = { deny: "text-danger", hum: "text-foreground", allow: "text-ok" };

const isLive = (row: PolicySetRow) => row.status === "active";

// Line is one row of the table: Everyone, an application role, or the
// Outside roles row, which names no role and so opens no door.
type Line = {
  key: string;
  name: string;
  mono: boolean;
  line: string;
  title?: string;
  // role is the name a door prefills; null is Everyone.
  role: string | null;
  doors: boolean;
  // none is what the Policies cell says when no policy governs the row.
  none: string;
  sets: PolicySetRow[];
};

// Head is one column header: the label, and the help icon on the one column
// that needs the sentence.
function Head({ id, right, help }: { id: keyof typeof ROLE_COLUMN; right?: boolean; help?: string }) {
  return (
    <span className={cn(HEAD, "inline-flex items-center gap-1", right && "justify-end")}>
      {ROLE_COLUMN[id]}
      {help && <HelpTip label={ROLE_COLUMN[id]} text={help} />}
    </span>
  );
}

// SetChip names one policy that governs the row, in its state: a policy
// that is off is muted and italic, a live policy a draft edits is amber, a
// live policy is green. A click opens the policy.
function SetChip({ row, onOpen }: { row: PolicySetRow; onOpen: () => void }) {
  const tone = !isLive(row) ? "italic text-muted-foreground" : row.drift ? "text-warn" : "text-ok";
  return (
    <button
      type="button"
      data-set={row.name}
      aria-label={"Open " + row.name}
      title={setChipTitle(row.status, row.drift)}
      className={cn("mr-1 mb-0.5 inline-block rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] hover:border-link", tone)}
      onClick={onOpen}
    >
      {row.name}
    </button>
  );
}

// Door is the dashed cell that opens New policy with this row's role and
// intent already chosen.
function Door({ label, title, onOpen }: { label: string; title: string; onOpen: () => void }) {
  return (
    <button
      type="button"
      title={title}
      data-door={label}
      className="rounded-md border border-dashed border-border px-1.5 py-px text-[13px] text-link hover:border-link hover:text-foreground"
      onClick={onOpen}
    >
      {label}
    </button>
  );
}

type Props = {
  rows: PolicySetRow[];
  // roles are the application roles, already narrowed by the search.
  roles: RoleRow[];
  // bindings say what each role reaches; null when they could not be read,
  // and the row then carries no reach on its hover.
  bindings: BindingRow[] | null;
  // lane narrows every count to one governed surface; "" counts them all.
  lane: string;
  onOpenPolicy: (name: string) => void;
};

export function PoliciesByRole({ rows, roles, bindings, lane, onOpenPolicy }: Props) {
  const rolesOf = (row: PolicySetRow) => (row.summary && row.summary.matchRoles) || [];
  const everyoneSets = rows.filter((r) => rolesOf(r).length === 0 && !(r.summary && r.summary.matchOther));
  const outsideSets = rows.filter((r) => rolesOf(r).length === 0 && r.summary && r.summary.matchOther);

  const servers = (role: string) => (bindings || []).filter((b) => b.role === role).map((b) => b.app).filter((a, i, xs) => xs.indexOf(a) === i).sort();

  const lines: Line[] = [
    { key: "everyone", name: EVERYONE, mono: false, line: EVERYONE_LINE, role: null, doors: true, none: CATEGORY.everyone.none, sets: everyoneSets },
    ...roles.map((r) => ({
      key: "role:" + r.name,
      name: r.name,
      mono: true,
      line: r.description || "",
      title: bindings ? reachLine(servers(r.name)) : undefined,
      role: r.name,
      doors: true,
      none: NO_ROLE_POLICY,
      sets: rows.filter((row) => rolesOf(row).includes(r.name)),
    })),
  ];
  if (outsideSets.length) {
    lines.push({ key: "outside", name: CATEGORY.outside.title, mono: false, line: CATEGORY.outside.line, role: null, doors: false, none: "", sets: outsideSets });
  }

  // A count reads one lane when the Lane filter names one, and the whole
  // policy otherwise.
  const postures = (row: PolicySetRow): Postures => {
    const s = row.summary;
    if (!s) return NO_POSTURES;
    return posturesOf(lane ? (s.lanes || {})[lane] : s.postures);
  };
  const sum = (sets: PolicySetRow[], bucket: Bucket) => sets.filter(isLive).reduce((n, s) => addPostures(n, postures(s)), NO_POSTURES)[bucket];

  // The floor is the everyone policy that denies for every session; it is
  // named in a role's hover, never added to the role's own number.
  const floor = everyoneSets.filter(isLive).find((s) => postures(s).deny > 0);

  const countTitleOf = (line: Line, bucket: Bucket) => {
    const words = (p: Postures) => (bucket === "deny" ? denyWords(p.deny) : bucket === "allow" ? allowWords(p.allow) : humSplit(p));
    const parts = line.sets.filter(isLive).filter((s) => postures(s)[bucket] > 0).map((s) => liveLine(s.name, words(postures(s))));
    if (bucket === "hum") parts.push(...line.sets.filter((s) => !isLive(s)).filter((s) => postures(s).hum > 0).map((s) => draftLine(s.name)));
    if (bucket === "deny" && line.role && floor) parts.push(FLOOR_LINE(floor.name, postures(floor).deny));
    return parts.join("\n");
  };

  // countCell is one of the three outcome cells: the number with its hover,
  // or the door when nothing denies or gates the row yet.
  const countCell = (line: Line, bucket: Bucket) => {
    const n = sum(line.sets, bucket);
    if (n === 0 && bucket !== "allow" && line.doors) {
      const deny = bucket === "deny";
      const title = line.role ? (deny ? doorDenyTitle(line.role) : doorApproveTitle(line.role)) : deny ? DOOR_DENY_EVERYONE_TITLE : DOOR_APPROVE_EVERYONE_TITLE;
      return <Door label={deny ? DOOR_DENY : DOOR_APPROVE} title={title} onOpen={() => openNewPolicy({ role: line.role, intent: deny ? "deny" : "approve", lane: lane || undefined })} />;
    }
    const title = n === 0 && bucket === "allow" && line.doors ? (line.role ? NO_ALLOW_ROLE : NO_ALLOW_EVERYONE) : countTitleOf(line, bucket);
    return (
      <span className={cn("font-mono tabular-nums", n ? TONE[bucket] : "text-muted-foreground")} title={title || undefined} data-count={bucket}>{n}</span>
    );
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="overflow-x-auto rounded-md border border-border bg-card">
        <Table className="table-fixed">
          <colgroup>
            {["role", "policies", "deny", "hum", "allow", "recording"].map((id) => <col key={id} style={{ width: WIDTHS[id] }} />)}
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10"><Head id="role" help={ROLE_HELP} /></TableHead>
              <TableHead className="h-10"><Head id="policies" /></TableHead>
              <TableHead className="h-10 text-right"><Head id="deny" right /></TableHead>
              <TableHead className="h-10 text-right"><Head id="hum" right /></TableHead>
              <TableHead className="h-10 text-right"><Head id="allow" right /></TableHead>
              <TableHead className="h-10"><Head id="recording" /></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {lines.map((line) => {
              const rec = line.sets.filter(isLive).find((s) => s.summary && s.summary.capture);
              return (
                <TableRow key={line.key} data-role={line.name} className="text-sm">
                  <TableCell title={line.title}>
                    <div className={cn("truncate text-foreground", line.mono ? "font-mono font-semibold" : "font-semibold")}>{line.name}</div>
                    {line.line && <div className="truncate text-[13px] text-muted-foreground" title={line.line}>{line.line}</div>}
                  </TableCell>
                  <TableCell>
                    {line.sets.length === 0
                      ? <span className="text-[13px] text-muted-foreground">{line.none}</span>
                      : line.sets.map((s) => <SetChip key={s.name} row={s} onOpen={() => onOpenPolicy(s.name)} />)}
                  </TableCell>
                  <TableCell className="text-right">{countCell(line, "deny")}</TableCell>
                  <TableCell className="text-right">{countCell(line, "hum")}</TableCell>
                  <TableCell className="text-right">{countCell(line, "allow")}</TableCell>
                  <TableCell>
                    {rec && rec.summary && rec.summary.capture
                      ? (
                        <span className="inline-flex items-center gap-1.5">
                          <RecChip capture={rec.summary.capture} />
                          <span className="text-[13px] text-muted-foreground">{recWord(rec.summary.capture)}</span>
                        </span>
                      )
                      : <span className="text-[13px] text-muted-foreground">{NO_RECORDING}</span>}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
      <p className="text-[13px] text-muted-foreground">{ROLE_FOOT}</p>
    </div>
  );
}
