import * as React from "react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { PolicyTest } from "@/components/policy-test";
import { UserAccessTab } from "@/components/user-access";
import { UserAgents } from "@/components/user-agents";
import { UserAssign } from "@/components/user-assign";
import { type Ask, UserConfirm } from "@/components/user-confirm";
import { UserDetails } from "@/components/user-details";
import { UserDevices } from "@/components/user-devices";
import { UserGlance } from "@/components/user-glance";
import { UserHead } from "@/components/user-head";
import { MUTED } from "@/components/user-parts";
import { UserSessions } from "@/components/user-sessions";
import { UserStrips } from "@/components/user-strips";
import type { Stop } from "@/components/user-stop-menu";
import {
  type ApiError, type ApprovalRow, type UserDetail, getConfig, getUser, listApprovals, listAssignments, listBindings, listRoles, listSessions, listTools, listUsers, lockUser, query, removeKey,
  revokeAssignment, revokeDevice, revokeUserSessions, setUserStatus, unlockUser, userPreview,
} from "@/lib/api";
import { keepsToast } from "@/lib/entra-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { RELOAD_NOW, SUBJECT_ROLES as SUBJECT_CATALOG } from "@/lib/role-words";
import { routeByKey } from "@/lib/routes";
import { readFailed, refused } from "@/lib/say";
import { adminAreas, snapshot } from "@/lib/session";
import { cutID, revokedWords } from "@/lib/session-words";
import { userAccess } from "@/lib/user-access";
import { entraOwns, refusal, userKind } from "@/lib/user-look";
import { dataOf, useRead } from "@/lib/user-read";
import {
  BACK_TO_USERS, DISABLE, ENABLE, LOCK, NO_SUCH_USER, READING_USER, REMOVE_ROLE, REVOKE_ALL_SESSIONS, REVOKE_DEVICE, REVOKE_KEY, SUBJECT_ACCESS, SUBJECT_AGENTS, SUBJECT_REQUESTS,
  SUBJECT_ROLES, SUBJECT_USER, UNLOCK, USER_TAB, deviceRevokedToast, disabledToast, enabledToast, keyRevokedToast, lockedToast, revokedRoleToast, stillLockedToast, unlockedToast,
} from "@/lib/user-words";

// The page of one user at users/<id>: the head with Test a call and Stop
// access, the strips of its state, the glance tiles, and the tabs Access,
// Sessions, Agents, Devices and keys, and Details, the tab in the address.
// The user is read first and every other read waits for it. Each further
// read is its own state, so a failed one never blanks the rest, and every
// read runs again after a write that landed.

const route = routeByKey("users");

type Props = { id: string; tab?: string };

type State = { kind: "loading" } | { kind: "missing" } | { kind: "error"; message: string } | { kind: "ready"; user: UserDetail; problem: string | null };

type Tab = keyof typeof USER_TAB;

// ACTIVE_FEW asks for a user's active sessions, two at most: enough to
// tell one session from several.
const ACTIVE_FEW = (id: string) => query({ user: id, status: "active", limit: 2 });

export function UserPage({ id, tab }: Props) {
  const areas = adminAreas();
  const may = (area: string) => areas === null || !!areas[area];
  const [nonce, setNonce] = React.useState(0);
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [problem, setProblem] = React.useState<{ subject: string; text: string } | null>(null);
  const [assigned, setAssigned] = React.useState<string | null>(null);
  const [assigning, setAssigning] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [toRoles, setToRoles] = React.useState(0);
  const opener = React.useRef<HTMLElement | null>(null);

  React.useEffect(() => {
    let alive = true;
    getUser(id).then(
      (user) => { if (alive) setState({ kind: "ready", user, problem: null }); },
      (e) => {
        const err = e as ApiError;
        if (!alive || err.status === 401) return;
        if (err.status === 404) { setState({ kind: "missing" }); return; }
        const message = readFailed(SUBJECT_USER, err);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
    return () => { alive = false; };
  }, [id, nonce]);

  const user = state.kind === "ready" ? state.user : null;
  const name = user ? user.username : "";
  const kind = user ? userKind(user) : "person";
  const sponsoring = !!user && kind === "person" && user.sponsored_count > 0;
  const sponsor = user && kind !== "person" ? user.sponsor || "" : "";
  const assignments = useRead(!!user, () => listAssignments(id), SUBJECT_ROLES, nonce);
  const roles = useRead(!!user, () => listRoles(), SUBJECT_CATALOG, nonce);
  const preview = useRead(!!user && may("apps"), () => userPreview(name), SUBJECT_ACCESS, nonce);
  const bindings = useRead(!!user && may("apps"), () => listBindings(), SUBJECT_ACCESS, nonce);
  const tools = useRead(!!user && may("apps"), () => listTools(), SUBJECT_ACCESS, nonce);
  const approvals = useRead(!!user && may("approvals"), () => listApprovals("pending", id), SUBJECT_REQUESTS, nonce);
  const agents = useRead(sponsoring, () => listUsers(query({ sponsor: name, limit: 50 })), SUBJECT_AGENTS, nonce);
  const sponsorRead = useRead(!!sponsor, () => listUsers(query({ q: sponsor, limit: 20 })), SUBJECT_USER, nonce);
  // listed are the people Entra lists as the sponsors of an agent that has
  // several, each looked up by object ID so the strip can name it. One the
  // read does not find shows by its ID, as every sponsor of the other
  // states does, since none of those is a person Straza has.
  const listed = user?.entra?.sponsor_state === "several" ? (user.entra.sponsors || []).filter((s) => s.kind === "user").map((s) => s.id) : [];
  const listedRead = useRead(listed.length > 0, () => Promise.all(listed.map((x) => listUsers(query({ q: x, limit: 5 })).then((p) => [x, (p.items || []).find((u) => u.external_id === x)?.username || ""] as [string, string]))), SUBJECT_USER, nonce);
  // The configuration says whether Entra still owns a user of origin entra.
  // It is read once, and quietly, since the page only uses it to leave out
  // what Straza would refuse.
  const config = useRead(!!user && user.origin === "entra" && may("config"), () => getConfig(), SUBJECT_USER, 0);

  const tabs: Tab[] = ["access", "sessions", ...(sponsoring ? ["agents" as const] : []), "devices", "details"];
  const current: Tab = tabs.includes(tab as Tab) ? (tab as Tab) : "access";
  const select = (t: string) => navigate("users", t === "access" ? [id] : [id, t], true);

  // The Roles tile selects Access and brings the Roles heading into view
  // once the tab has drawn it. Selected from another tab, the panel draws
  // its content a render after this effect runs, so the effect watches the
  // page until the heading is there.
  React.useEffect(() => {
    if (!toRoles || current !== "access") return;
    const scroll = () => {
      const head = document.querySelector("[data-roles-head]");
      if (!head) return false;
      setToRoles(0);
      head.scrollIntoView?.({ block: "start", behavior: "smooth" });
      return true;
    };
    if (scroll()) return;
    const watch = new MutationObserver(() => { if (scroll()) watch.disconnect(); });
    watch.observe(document.body, { childList: true, subtree: true });
    return () => watch.disconnect();
  }, [toRoles, current]);

  if (state.kind !== "ready" || !user) {
    return (
      <>
        <PageHead label={cutID(id)} crumbs={[{ label: route.label, to: "users" }]} title={route.label} description="" />
        <div className="flex flex-col items-start gap-3 px-6 py-5">
          {state.kind === "loading" && <p className="m-0 text-dense text-muted-foreground" role="status">{READING_USER}</p>}
          {state.kind === "missing" && <p className="m-0 text-dense text-text-2" data-no-such-user>{NO_SUCH_USER}</p>}
          {state.kind === "missing" && <Button variant="link" className="h-auto p-0 text-link" onClick={() => navigate("users")}>{BACK_TO_USERS}</Button>}
          {state.kind === "error" && <FetchError subject={SUBJECT_USER} detail={state.message} />}
          {state.kind === "error" && <Button variant="outline" size="sm" onClick={() => { setState({ kind: "loading" }); setNonce((n) => n + 1); }}>{RELOAD_NOW}</Button>}
        </div>
      </>
    );
  }

  const answer = dataOf(preview);
  const access = answer ? userAccess(answer, dataOf(bindings) || [], dataOf(roles) || [], dataOf(assignments) || []) : null;
  const requests = dataOf(approvals) ? dataOf(approvals)?.approvals || [] : null;
  const sponsorID = (dataOf(sponsorRead)?.items || []).find((u) => u.username === sponsor)?.id || null;
  const scim = user.origin === "scim";
  const entraOwned = entraOwns(user, dataOf(config) || undefined);
  const activeCount = user.counts?.active_sessions || 0;

  // run makes one write. A write that landed closes its confirm, says so
  // and reads the page again. A refusal stays where the write was asked.
  const run = async (tag: string, subject: string, write: () => Promise<unknown>, ok: string | null) => {
    if (busy) return;
    setBusy(tag);
    setProblem(null);
    try {
      await write();
      setAsk(null);
      if (ok) notify.ok(ok);
      setNonce((n) => n + 1);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem({ subject, text: refused(err) });
    } finally {
      setBusy(null);
    }
  };

  // keep holds what has the focus as a dialog opens, so the focus can
  // return there when the dialog closes. A choice of the Stop access menu
  // is gone by then, so its button stands in for it. open asks a confirm.
  const keep = () => {
    const from = document.activeElement as HTMLElement | null;
    opener.current = from && from.closest("[data-stop-menu]") ? document.querySelector<HTMLElement>("[data-stop-trigger]") : from;
  };
  const open = (next: Ask) => {
    keep();
    setProblem(null);
    setAsk(next);
  };
  const restore = () => {
    const to = opener.current && opener.current.isConnected ? opener.current : document.querySelector<HTMLElement>("main h1");
    to?.focus();
  };

  const onStop = (stop: Stop) => open({ kind: stop });

  const go = (reason: string) => {
    if (!ask) return;
    if (ask.kind === "lock") void run("lock", LOCK, () => lockUser(id, reason), lockedToast(name));
    else if (ask.kind === "unlock") void run("unlock", UNLOCK, () => unlockUser(id), unlockedToast(name));
    else if (ask.kind === "disable") void run("disable", DISABLE, () => setUserStatus(id, "disabled"), disabledToast(name));
    else if (ask.kind === "removeRole") void run("remove", REMOVE_ROLE, () => revokeAssignment(ask.row.id), ask.fromEntra ? keepsToast(name, ask.role) : revokedRoleToast(name, ask.role));
    else if (ask.kind === "device") void run("device", REVOKE_DEVICE, () => revokeDevice(id, ask.device.id), deviceRevokedToast(ask.label));
    else if (ask.kind === "revokeKey") void run("key", REVOKE_KEY, () => removeKey(id), keyRevokedToast(name));
    else void run("sessions", REVOKE_ALL_SESSIONS, async () => { const r = await revokeUserSessions(id); notify.ok(revokedWords(r?.revoked ?? activeCount)); }, null);
  };

  // openSessions follows the banner's button: the one active session's
  // page when there is one, else the Sessions tab.
  const openSessions = () => {
    if (activeCount !== 1) { select("sessions"); return; }
    listSessions(ACTIVE_FEW(id)).then(
      (p) => { const items = p.items || []; if (items.length === 1) navigate("sessions", [items[0].id]); else select("sessions"); },
      () => select("sessions"),
    );
  };

  const onRequests = (rows: ApprovalRow[]) => (rows.length === 1 ? navigate("approvals", ["requests", rows[0].id]) : navigate("approvals"));
  // The reader's own console session is one of the user's active sessions
  // when the page is the reader's own.
  const own = snapshot()?.user === name;

  return (
    <>
      <UserHead user={user} sponsorID={sponsorID} canTest={may("policy")} sessions={may("sessions")} entraOwned={entraOwned} onTest={() => { keep(); setTesting(true); }} onStop={onStop} />
      <div className="flex flex-col gap-4 px-6 py-5">
        {state.problem && <FetchError subject={SUBJECT_USER} detail={state.problem} />}
        <UserStrips
          user={user}
          entraOwned={entraOwned}
          sponsorNames={Object.fromEntries(dataOf(listedRead) || [])}
          assigned={assigned}
          sessions={may("sessions")}
          enabling={busy === "enable"}
          refused={!ask && !assigning ? problem : null}
          onUnlock={() => open({ kind: "unlock" })}
          onEnable={() => void run("enable", ENABLE, async () => { const r = await setUserStatus(id, "active"); if (r?.notice) notify.warn(stillLockedToast(name)); else notify.ok(enabledToast(name)); }, null)}
          onOpenSessions={openSessions}
          onDismiss={() => setAssigned(null)}
        />
        <UserGlance user={user} access={refusal(user, answer?.refused) ? null : access} requests={requests} onRoles={() => { select("access"); setToRoles((n) => n + 1); }} onTab={select} onRequests={onRequests} />
        <Tabs value={current} onValueChange={select}>
          <div className="flex min-w-0 items-center border-b border-border">
            <TabsList variant="line" className="max-w-full justify-start overflow-x-auto">
              {tabs.map((t) => {
                const n = t === "sessions" ? activeCount : t === "agents" ? user.sponsored_count : 0;
                return <TabsTrigger key={t} value={t} className="flex-none">{USER_TAB[t]}{n > 0 && <span className={MUTED + " font-mono leading-4"}>{n}</span>}</TabsTrigger>;
              })}
            </TabsList>
          </div>
          <TabsContent value="access">
            <UserAccessTab user={user} servers={may("apps")} audit={may("audit")} policy={may("policy")} preview={preview} bindings={bindings} roles={roles} assignments={assignments} agents={agents} access={access} onAssign={() => { keep(); setAssigning(true); }} onRemove={(row, role, fromEntra) => open({ kind: "removeRole", row, role, fromEntra })} />
          </TabsContent>
          <TabsContent value="sessions">
            <UserSessions id={id} name={name} allowed={may("sessions")} active={activeCount} nonce={nonce} onRevokeAll={() => open({ kind: "sessions" })} />
          </TabsContent>
          {sponsoring && <TabsContent value="agents"><UserAgents name={name} agents={agents} /></TabsContent>}
          <TabsContent value="devices">
            <UserDevices user={user} nonce={nonce} onRevokeDevice={(device, label) => open({ kind: "device", device, label })} onRevokeKey={() => open({ kind: "revokeKey" })} onChanged={() => setNonce((n) => n + 1)} />
          </TabsContent>
          <TabsContent value="details"><UserDetails user={user} sponsorID={sponsorID} /></TabsContent>
        </Tabs>
      </div>
      <UserConfirm ask={ask} name={name} scim={scim} busy={!!busy} problem={problem} sessions={activeCount} own={own} onGo={go} onClose={() => { setAsk(null); setProblem(null); }} onClosed={restore} />
      <UserAssign
        open={assigning}
        onOpenChange={setAssigning}
        userID={id}
        name={name}
        machine={kind !== "person"}
        scim={scim}
        servers={may("apps")}
        roles={roles.kind === "failed" ? null : dataOf(roles) || []}
        reading={roles.kind === "loading"}
        assignments={dataOf(assignments) || []}
        bindings={may("apps") ? dataOf(bindings) : null}
        tools={may("apps") ? dataOf(tools) : null}
        onAssigned={(role) => { setAssigning(false); setAssigned(role); setNonce((n) => n + 1); }}
        onClosed={restore}
      />
      {may("policy") && <PolicyTest open={testing} onOpenChange={setTesting} prefill={{ user: name }} onClosed={restore} />}
    </>
  );
}
