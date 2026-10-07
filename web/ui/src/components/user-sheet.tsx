import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Facts, Glance, ListRow, Section, StateBox, StateRow } from "@/components/sheet-parts";
import { NoRole, RoleChip } from "@/components/status-badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { KindBadge, OriginBadge, WordBadge } from "@/components/users-table";
import { type ApiError, type AssignmentRow, type DeviceRow, type KeyPosture, type Page, type RoleRow, type UserDetail, type UserRow, getUser, grantRole, keyPosture, listAssignments, listDevices, listUsers, lockUser, query, removeKey, revokeAssignment, revokeDevice, setKey, setUserStatus, unlockUser } from "@/lib/api";
import { put } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { readFailed, refused } from "@/lib/say";
import {
  CHECKIN_HELP, DEVICE_HELP, DEVICES_LINE, DISABLE_HELP, DISABLE_TIP, DRIFT_HELP, DRIFT_LINE, ENABLE_TIP, GRANT_HELP, GRANT_MISSING, GRANT_PICK, IMPLIED_LINE, KEY_HINT, KEY_LABEL, KEY_MISSING, KEY_REVOKE_HELP, LOCK_HELP, LOCK_TIP, NO_DEVICE, NO_KEY_LINE, NO_ROLE, ORIGIN_WORDS,
  READING_AGENTS, READING_DEVICES, READING_KEY, READING_ROLES, READING_USER, REASON_LABEL, REASON_MISSING, SUBJECT_AGENTS, SUBJECT_DEVICES, SUBJECT_KEY, SUBJECT_ROLES, SUBJECT_USER, UNLOCK_HELP, UNLOCK_TIP,
  deadRowBody, deviceRevokeBody, deviceRevokedToast, disableBody, disabledToast, driftBody, enableBody, grantBody, grantedToast, keyRevokeBody, keyRevokedToast, keySetToast, kindWord, lockBody, lockedLine, lockedToast, nothingToGrant, originLine, provisionedWords, revokeRoleBody, revokedRoleToast, sponsoringWords, unlockBody, unlockedToast, windowWord,
} from "@/lib/user-words";
import { absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The user sheet: the lock and
// the status as two state rows with their one verb each, the facts, the
// numbers, then the lists. It carries no explanatory paragraph; the
// sentence that explains a verb sits in that verb's dialog or its tooltip.

type Props = {
  // user is the list row, kept fresh by the list's poll.
  user: UserRow;
  // roles is the catalog the Grant picker offers from.
  roles: RoleRow[];
  // rolesStale says the catalog read failed, so the picker offers nothing
  // and says why rather than claiming no role exists.
  rolesStale?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onChanged reloads the list after a write that landed.
  onChanged: () => void;
  onFilterSessions?: (userID: string) => void;
};

const MUTED = "text-[13px] text-muted-foreground";
const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg";
const DESTRUCTIVE = "bg-danger text-white hover:bg-danger/90";

// Read is one sub-read of the sheet. Each read carries its own word and
// its own failure, so a device list that did not answer never blanks the
// roles beside it.
type Read<T> = { kind: "off" } | { kind: "loading" } | { kind: "ready"; data: T } | { kind: "failed"; message: string };

function useRead<T>(on: boolean, read: () => Promise<T>, subject: string, nonce: number): Read<T> {
  const [state, setState] = React.useState<Read<T>>(on ? { kind: "loading" } : { kind: "off" });
  React.useEffect(() => {
    if (!on) { setState({ kind: "off" }); return; }
    let alive = true;
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    read().then(
      (data) => { if (alive) setState({ kind: "ready", data }); },
      (e) => {
        const err = e as ApiError;
        if (alive && err.status !== 401) setState({ kind: "failed", message: readFailed(subject, err) });
      },
    );
    return () => { alive = false; };
  }, [on, nonce]); // eslint-disable-line react-hooks/exhaustive-deps
  return state;
}

// Waiting renders what a sub-read has to say while it is out or after it
// failed, and nothing once it answered.
function Waiting<T>({ read, word, subject }: { read: Read<T>; word: string; subject: string }) {
  if (read.kind === "loading") return <p className={MUTED} role="status">{word}</p>;
  if (read.kind === "failed") return <FetchError subject={subject} detail={read.message} />;
  return null;
}

// Ask is the confirm a verb opens. Every one of them restates the object.
type Ask =
  | { kind: "lock" }
  | { kind: "unlock" }
  | { kind: "disable" }
  | { kind: "grant"; role: RoleRow }
  | { kind: "revokeRole"; row: AssignmentRow; role: string }
  | { kind: "device"; device: DeviceRow }
  | { kind: "revokeKey" };

export function UserSheet(props: Props) {
  const { user, open, onOpenChange } = props;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[560px]" data-user-sheet={user.username}>
        {open && <Body key={user.id} {...props} />}
      </SheetContent>
    </Sheet>
  );
}

function Body({ user, roles, rolesStale = false, onOpenChange, onChanged, onFilterSessions }: Props) {
  const [nonce, setNonce] = React.useState(0);
  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [problem, setProblem] = React.useState<{ subject: string; text: string } | null>(null);
  const [reason, setReason] = React.useState("");
  const [reasonMiss, setReasonMiss] = React.useState(false);
  const [pick, setPick] = React.useState("");
  const [grantMiss, setGrantMiss] = React.useState(false);
  const [keyForm, setKeyForm] = React.useState<"" | "register" | "rotate">("");
  const [keyValue, setKeyValue] = React.useState("");
  const [keyMiss, setKeyMiss] = React.useState(false);
  const reasonRef = React.useRef<HTMLInputElement>(null);
  const grantRef = React.useRef<HTMLButtonElement>(null);
  const keyRef = React.useRef<HTMLInputElement>(null);

  const name = user.username;
  const agent = kindWord(user) === "AI agent";
  const person = kindWord(user) === "person";
  const scim = user.origin === "scim";

  const detailRead = useRead<UserDetail>(true, () => getUser(user.id), SUBJECT_USER, nonce);
  const assignRead = useRead<AssignmentRow[]>(true, () => listAssignments(user.id), SUBJECT_ROLES, nonce);
  const deviceRead = useRead<DeviceRow[]>(true, () => listDevices(user.id), SUBJECT_DEVICES, nonce);
  const keyRead = useRead<KeyPosture>(agent, () => keyPosture(user.id), SUBJECT_KEY, nonce);
  const agentRead = useRead<Page<UserRow>>(user.sponsored_count > 0, () => listUsers(query({ sponsor: name, limit: 50 })), SUBJECT_AGENTS, nonce);

  const detail = detailRead.kind === "ready" ? detailRead.data : null;
  // The detail is the truth once it lands; until then the list row already
  // carries the status, the locks and the facts.
  const shown: UserRow = detail || user;
  const locks = shown.locks || [];
  const active = shown.status === "active";
  const assignments = assignRead.kind === "ready" ? assignRead.data : [];
  const devices = deviceRead.kind === "ready" ? deviceRead.data : [];
  const key = keyRead.kind === "ready" ? keyRead.data : null;
  const sponsored = agentRead.kind === "ready" ? agentRead.data.items || [] : [];
  const roleByID = React.useMemo(() => new Map(roles.map((r) => [r.id, r])), [roles]);
  const held = new Set(assignments.filter((a) => windowWord(a) === "").map((a) => a.role_id));
  const offer = roles.filter((r) => !held.has(r.id));

  const asking = ask !== null;
  const run = async (tag: string, subject: string, fn: () => Promise<unknown>, ok: string) => {
    if (busy) return;
    setBusy(tag);
    setProblem(null);
    try {
      await fn();
      setAsk(null);
      notify.ok(ok);
      setNonce((n) => n + 1);
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      // A 401 belongs to the session module, which takes the app to sign-in.
      if (err.status === 401) return;
      const text = refused(err);
      setProblem({ subject, text });
      if (!asking) notify.failed(text);
    } finally {
      setBusy(null);
    }
  };

  const lock = () => {
    if (!reason.trim()) { setReasonMiss(true); reasonRef.current?.focus(); return; }
    setReasonMiss(false);
    void run("lock", "Lock user", () => lockUser(user.id, reason.trim()), lockedToast(name));
  };
  const unlock = () => void run("unlock", "Unlock user", () => unlockUser(user.id), unlockedToast(name));
  const disable = () => void run("disable", "Disable user", () => setUserStatus(user.id, "disabled"), disabledToast(name));
  const enable = () => void run("enable", "Enable user", () => setUserStatus(user.id, "active"), enableBody(name));
  const grant = (role: RoleRow) => void run("grant", "Assign role", () => grantRole(user.id, role.id), grantedToast(name, role.name));
  const revokeRole = (row: AssignmentRow, role: string) => void run("revoke-role", "Revoke role", () => revokeAssignment(row.id), revokedRoleToast(name, role));
  const revokeOne = (device: DeviceRow) => void run("device", "Revoke device", () => revokeDevice(user.id, device.id), deviceRevokedToast(deviceName(device)));
  const dropKey = () => void run("key-revoke", "Revoke key", () => removeKey(user.id), keyRevokedToast(name));
  const askGrant = () => {
    const role = roles.find((r) => r.id === pick);
    if (!role) { setGrantMiss(true); grantRef.current?.focus(); return; }
    setGrantMiss(false);
    setAsk({ kind: "grant", role });
  };
  const submitKey = () => {
    const value = keyValue.trim();
    if (!value) { setKeyMiss(true); keyRef.current?.focus(); return; }
    setKeyMiss(false);
    void run("key-set", keyForm === "rotate" ? "Rotate key" : "Register key", async () => {
      await setKey(user.id, value);
      setKeyForm("");
      setKeyValue("");
    }, keySetToast(name));
  };

  const seenWords = (iso: string | undefined) => {
    if (!iso) return <span className="text-muted-foreground">never</span>;
    const rel = relTimeText(iso);
    const abs = absTime(iso);
    return <span>{rel === abs ? abs : rel + ", " + abs}</span>;
  };

  const facts: [string, React.ReactNode][] = [
    ["Email", shown.email || <span className="text-muted-foreground">none</span>],
    ["Origin", originLine(shown)],
    ["Provisioned", absTime(shown.created_at)],
    ["Last seen", seenWords(shown.last_seen)],
  ];
  if (agent) {
    facts.push(["Agency", shown.agency_mode || <span className="text-muted-foreground">not set</span>]);
    facts.push(["Sponsor", shown.sponsor ? <span className="font-mono">{shown.sponsor}</span> : <span className="text-muted-foreground">none</span>]);
    if (shown.swarm_id) facts.push(["Swarm", <span className="font-mono">{shown.swarm_id}</span>]);
    if (shown.ephemeral) facts.push(["Ephemeral", "yes"]);
  }
  if (person) facts.push(["Sponsoring", sponsoringWords(shown.sponsored_count || 0)]);

  const counts = detail?.counts;
  const busyIcon = (tag: string) => (busy === tag ? <Loader2Icon className="animate-spin" /> : null);

  return (
    <>
      <SheetHeader className="border-b border-border pr-12">
        <SheetTitle className="flex flex-wrap items-center gap-2 text-lg leading-snug">
          <span className="font-mono">{name}</span>
          <KindBadge user={shown} />
          <OriginBadge origin={shown.origin} />
        </SheetTitle>
        <SheetDescription>
          {shown.display || shown.email ? [shown.display, shown.email].filter(Boolean).join(", ") : provisionedWords(absTime(shown.created_at))}
        </SheetDescription>
      </SheetHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4" data-user-body>
        {!asking && problem && <RefusedError subject={problem.subject} message={problem.text} />}

        <StateBox>
          {locks.length === 0 ? (
            <StateRow
              label="Straza lock"
              action={<Button variant="outline" size="sm" className={DANGER_BUTTON} title={LOCK_TIP} onClick={() => { setReason(""); setReasonMiss(false); setProblem(null); setAsk({ kind: "lock" }); }}>Lock user</Button>}
            >
              <span className="text-muted-foreground">none</span>
            </StateRow>
          ) : (
            <StateRow
              label="Straza lock"
              tone="danger"
              action={<Button variant="outline" size="sm" title={UNLOCK_TIP} onClick={() => { setProblem(null); setAsk({ kind: "unlock" }); }}>Unlock user</Button>}
            >
              {locks.map((l, i) => (
                <span key={l.origin + l.created_at + i} className="block" data-lock-line>
                  <b className="font-semibold">{lockedLine(l.origin)}</b>
                  {l.reason ? " " + l.reason : ""}{" "}
                  <span className="text-muted-foreground">{relTimeText(l.created_at)}</span>
                </span>
              ))}
            </StateRow>
          )}
          <StateRow
            label="Status"
            action={active
              ? <Button variant="outline" size="sm" className={DANGER_BUTTON} title={DISABLE_TIP} onClick={() => { setProblem(null); setAsk({ kind: "disable" }); }}>Disable user</Button>
              : <Button variant="outline" size="sm" title={ENABLE_TIP} disabled={busy === "enable"} onClick={enable}>{busyIcon("enable")}Enable user</Button>}
          >
            <WordBadge word={shown.status} tone={active ? "ok" : "danger"} mono attr="data-sheet-status" />
          </StateRow>
        </StateBox>

        <Facts rows={facts} />

        <Waiting read={detailRead} word={READING_USER} subject={SUBJECT_USER} />
        {counts && (
          <Glance
            items={[
              { n: counts.sessions, label: "sessions, " + counts.active_sessions + " active", onOpen: onFilterSessions ? () => onFilterSessions(user.id) : undefined },
              { n: counts.devices, label: counts.devices === 1 ? "device" : "devices" },
              { n: counts.approver_devices, label: counts.approver_devices === 1 ? "approver phone" : "approver phones" },
              { n: counts.approvals, label: "approvals raised" },
            ]}
          />
        )}

        {user.sponsored_count > 0 && (
          <Section title="Sponsored agents">
            <Waiting read={agentRead} word={READING_AGENTS} subject={SUBJECT_AGENTS} />
            {sponsored.map((a) => (
              <ListRow key={a.id} actions={<WordBadge word={a.status} tone={a.status === "active" ? "ok" : "danger"} mono />}>
                <span className="font-mono">{a.username}</span>
                <KindBadge user={a} />
                {a.effective_roles && a.effective_roles.length > 0 && <RoleChip name={a.effective_roles[0]} />}
                <span className={MUTED}>{a.last_seen ? "seen " + relTimeText(a.last_seen) : "never seen"}</span>
              </ListRow>
            ))}
          </Section>
        )}

        <Section title="Roles">
          <Waiting read={assignRead} word={READING_ROLES} subject={SUBJECT_ROLES} />
          {assignments.map((a) => {
            const role = roleByID.get(a.role_id)?.name || a.role_id;
            const dead = windowWord(a);
            return (
              <ListRow key={a.id} actions={
                <Button variant="outline" size="sm" className={DANGER_BUTTON} aria-label={"Revoke " + role} onClick={() => { setProblem(null); setAsk({ kind: "revokeRole", row: a, role }); }}>Revoke</Button>
              }>
                <RoleChip name={role} />
                <span className={MUTED}>{ORIGIN_WORDS[a.origin || "admin"] || a.origin}</span>
                {dead && <WordBadge word={dead} tone="warn" attr="data-dead-row" />}
              </ListRow>
            );
          })}
          {assignRead.kind === "ready" && assignments.length === 0 && <div><NoRole text={NO_ROLE} /></div>}
          {offer.length === 0 || rolesStale ? (
            <p className={MUTED}>{nothingToGrant(roles.length === 0, rolesStale)}</p>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <Select value={pick} onValueChange={(v) => { setPick(v); setGrantMiss(false); }}>
                  <SelectTrigger ref={grantRef} size="sm" aria-label="Assign" className="w-[240px]" data-grant-pick>
                    <span className="text-muted-foreground">Assign</span>
                    <SelectValue placeholder={GRANT_PICK} />
                  </SelectTrigger>
                  <SelectContent>
                    {offer.map((r) => <SelectItem key={r.id} value={r.id} className="font-mono">{r.name}</SelectItem>)}
                  </SelectContent>
                </Select>
                <Button variant="outline" size="sm" onClick={askGrant}>Assign role</Button>
              </div>
              {grantMiss && <p className="text-[13px] text-danger" role="alert">{GRANT_MISSING}</p>}
            </>
          )}
          {scim && <p className={MUTED}>{DRIFT_LINE}</p>}
          <p className={MUTED}>{IMPLIED_LINE}</p>
        </Section>

        <Section title="Devices" action={
          <Tooltip>
            <TooltipTrigger asChild>
              <button type="button" className={cn(MUTED, "underline decoration-dotted underline-offset-4")} data-devices-tip>what a device is</button>
            </TooltipTrigger>
            <TooltipContent className="max-w-[44ch]">{DEVICES_LINE}</TooltipContent>
          </Tooltip>
        }>
          <Waiting read={deviceRead} word={READING_DEVICES} subject={SUBJECT_DEVICES} />
          {devices.map((d) => (
            <ListRow key={d.id} actions={
              <>
                <WordBadge word={d.status} tone={d.status === "active" ? "ok" : "danger"} mono />
                <Button variant="outline" size="sm" className={DANGER_BUTTON} aria-label={"Revoke " + deviceName(d)} onClick={() => { setProblem(null); setAsk({ kind: "device", device: d }); }}>Revoke</Button>
              </>
            }>
              <b className="font-semibold">{deviceName(d)}</b>
              <WordBadge word={d.platform || "unknown"} tone="plain" mono />
              <span className={MUTED}>{"enrolled " + absTime(d.enrolled_at)}</span>
            </ListRow>
          ))}
          {deviceRead.kind === "ready" && devices.length === 0 && <p className={MUTED}>{NO_DEVICE}</p>}
        </Section>

        {agent && (
          <Section title="Assertion key">
            <Waiting read={keyRead} word={READING_KEY} subject={SUBJECT_KEY} />
            {key && key.registered && (
              <ListRow actions={
                <>
                  {keyForm === "" && <Button variant="outline" size="sm" onClick={() => { setKeyValue(""); setKeyMiss(false); setKeyForm("rotate"); }}>Rotate key</Button>}
                  <Button variant="outline" size="sm" className={DANGER_BUTTON} onClick={() => { setProblem(null); setAsk({ kind: "revokeKey" }); }}>Revoke key</Button>
                </>
              }>
                <WordBadge word="registered" tone="ok" attr="data-key" />
                <span className={MUTED}>{"since " + relTimeText(key.created)}</span>
                <span className="truncate font-mono text-[13px]">{key.fingerprint}</span>
              </ListRow>
            )}
            {key && !key.registered && (
              <ListRow actions={keyForm === "" ? <Button size="sm" onClick={() => { setKeyValue(""); setKeyMiss(false); setKeyForm("register"); }}>Register key</Button> : null}>
                <WordBadge word="no key" tone="warn" attr="data-key" />
                <span>{NO_KEY_LINE}</span>
              </ListRow>
            )}
            {keyForm !== "" && (
              <div className="flex flex-col gap-1.5 rounded-md border border-border px-3 py-3" data-key-form={keyForm}>
                <Label htmlFor="user-key">{KEY_LABEL}</Label>
                <Input id="user-key" ref={keyRef} value={keyValue} className="font-mono" autoFocus onChange={(e) => { setKeyValue(e.target.value); setKeyMiss(false); }} />
                <p className={MUTED}>{KEY_HINT}</p>
                {keyMiss && <p className="text-[13px] text-danger" role="alert">{KEY_MISSING}</p>}
                <div className="flex items-center justify-end gap-2">
                  <Button variant="outline" size="sm" onClick={() => { setKeyForm(""); setKeyMiss(false); }}>Cancel</Button>
                  <Button size="sm" disabled={busy === "key-set"} onClick={submitKey}>{busyIcon("key-set")}{keyForm === "rotate" ? "Rotate key" : "Register key"}</Button>
                </div>
              </div>
            )}
          </Section>
        )}
      </div>

      <SheetFooter className="mt-0 border-t border-border">
        <div className="flex items-center justify-between gap-2">
          <span className={MUTED}>
            {"Audit trail: "}
            <Button variant="link" size="sm" className="h-auto p-0 text-[13px] text-link" aria-label="Open the audit trail" onClick={() => { put("audit", { user: name }); navigate("audit"); }}>open</Button>
          </span>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
        </div>
      </SheetFooter>

      <AlertDialog open={asking} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setProblem(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          {ask && (
            <Confirm
              ask={ask}
              name={name}
              scim={scim}
              busy={busy}
              problem={problem}
              reason={reason}
              reasonMiss={reasonMiss}
              reasonRef={reasonRef}
              onReason={(v) => { setReason(v); setReasonMiss(false); }}
              onGo={() => {
                if (ask.kind === "lock") lock();
                else if (ask.kind === "unlock") unlock();
                else if (ask.kind === "disable") disable();
                else if (ask.kind === "grant") grant(ask.role);
                else if (ask.kind === "revokeRole") revokeRole(ask.row, ask.role);
                else if (ask.kind === "device") revokeOne(ask.device);
                else dropKey();
              }}
            />
          )}
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// deviceName is the device's own name, or the head of its id when the
// enrolment carried none.
function deviceName(d: DeviceRow): string {
  return d.name || d.id.slice(0, 8);
}

type ConfirmProps = {
  ask: Ask;
  name: string;
  scim: boolean;
  busy: string | null;
  problem: { subject: string; text: string } | null;
  reason: string;
  reasonMiss: boolean;
  reasonRef: React.RefObject<HTMLInputElement | null>;
  onReason: (v: string) => void;
  onGo: () => void;
};

// Confirm is the one dialog of the sheet. It restates the object in its
// title, says what changes in one or two sentences, keeps the mechanics
// behind the help icon, and keeps its primary clickable so an empty field
// is answered at the field.
function Confirm({ ask, name, scim, busy, problem, reason, reasonMiss, reasonRef, onReason, onGo }: ConfirmProps) {
  const say = words(ask, name, scim);
  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>{say.title}</AlertDialogTitle>
        <AlertDialogDescription>
          {say.body}
          {say.help && <HelpTip label={say.label} text={say.help} className="ml-1" />}
        </AlertDialogDescription>
      </AlertDialogHeader>
      {ask.kind === "lock" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="lock-reason">{REASON_LABEL}</Label>
          <Input id="lock-reason" ref={reasonRef} value={reason} autoFocus onChange={(e) => onReason(e.target.value)} />
          {reasonMiss && <p className="text-[13px] text-danger" role="alert">{REASON_MISSING}</p>}
        </div>
      )}
      {problem && <RefusedError subject={problem.subject} message={problem.text} />}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={!!busy}>Cancel</AlertDialogCancel>
        <AlertDialogAction
          className={say.destructive ? DESTRUCTIVE : undefined}
          disabled={!!busy}
          onClick={(e) => { e.preventDefault(); onGo(); }}
        >
          {busy && <Loader2Icon className="animate-spin" />}{say.label}
        </AlertDialogAction>
      </AlertDialogFooter>
    </>
  );
}

// words is the copy of one confirm: the title names the object, the body
// says what changes, the help carries the mechanics, and the label repeats
// the verb.
function words(ask: Ask, name: string, scim: boolean): { title: string; body: string; help: string; label: string; destructive: boolean } {
  const drift = scim ? " " + DRIFT_HELP : "";
  switch (ask.kind) {
    case "lock":
      return { title: "Lock " + name + "?", body: lockBody(name), help: LOCK_HELP, label: "Lock user", destructive: true };
    case "unlock":
      return { title: "Unlock " + name + "?", body: unlockBody(name), help: UNLOCK_HELP, label: "Unlock user", destructive: false };
    case "disable":
      return { title: "Disable " + name + "?", body: disableBody(name), help: DISABLE_HELP, label: "Disable user", destructive: true };
    case "grant":
      return { title: "Assign " + ask.role.name + " to " + name + "?", body: grantBody(name, ask.role.name) + (scim ? driftBody(ask.role.name, "assignment") : ""), help: GRANT_HELP + drift, label: "Assign role", destructive: false };
    case "revokeRole": {
      const dead = windowWord(ask.row);
      const body = (dead ? deadRowBody(ask.role, dead) : revokeRoleBody(name, ask.role)) + (scim ? driftBody(ask.role, "revoke") : "");
      return { title: "Revoke " + ask.role + " from " + name + "?", body, help: CHECKIN_HELP + drift, label: "Revoke role", destructive: true };
    }
    case "device":
      return { title: "Revoke " + deviceName(ask.device) + "?", body: deviceRevokeBody(name, deviceName(ask.device)), help: DEVICE_HELP, label: "Revoke device", destructive: true };
    default:
      return { title: "Revoke the assertion key of " + name + "?", body: keyRevokeBody(name), help: KEY_REVOKE_HELP, label: "Revoke key", destructive: true };
  }
}
