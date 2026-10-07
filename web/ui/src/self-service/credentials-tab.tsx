// The Credentials tab of the self-service page: one table of the servers
// the person's roles reach and the same
// rows for each agent they sponsor, banded by what each row needs. The tab
// rides the login session alone; deciding, which rides the device key, is
// untouched by everything here. Every write redraws its row from the
// server's answer, so nothing on screen is this page's guess. The table
// itself is credential-table.tsx; what is here is the reading, the writing
// and what the person sees while either is in flight.
import * as React from "react";
import { KeyRoundIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { type ApiError, type ConnectAnswer, connectAgents, connectFinish, connectStart, selfServers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { bare, readFailed, refused } from "@/lib/say";
import { refresh } from "@/lib/session";
import { cn } from "@/lib/utils";
import { type ComeBack, leaveFor, takeComeBack } from "./come-back";
import { useSelf } from "./context";
import { type Ask, ConfirmDialog } from "./credential-dialogs";
import { type CredentialRow, keyOf, needing, sortRows, thingOf } from "./credential-rows";
import { PasteSheet } from "./credential-sheet";
import { type Cells, CredentialTable, type RowNote } from "./credential-table";
import * as W from "./credential-words";
import { SIGN_IN } from "./words";

export function CredentialsTab() {
  const { session, self, openSignIn, setCount, setNotice } = useSelf();
  const [rows, setRows] = React.useState<CredentialRow[] | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [lastRead, setLastRead] = React.useState<Date | null>(null);
  const [filter, setFilter] = React.useState<string>("all");
  const [sheet, setSheet] = React.useState<CredentialRow | null>(null);
  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [back, setBack] = React.useState<ComeBack | null>(null);
  const [finishing, setFinishing] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  // reads counts the re-reads a finished sign-in asked for.
  const [reads, setReads] = React.useState(0);
  const [note, setNote] = React.useState<RowNote | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);

  const signedIn = session.kind === "signed-in";
  const me = session.kind === "signed-in" ? session.user : "";
  const read = self !== null;
  const sponsored = self && self !== "error" ? self.sponsored || [] : [];
  const sponsoredKey = sponsored.join(",");

  // kept mirrors the rows for the reads that must not lose them: an agent
  // whose list could not be re-read keeps the one it had.
  const kept = React.useRef<CredentialRow[]>([]);
  React.useEffect(() => { kept.current = rows || []; }, [rows]);

  const patch = React.useCallback((row: CredentialRow, fields: Partial<CredentialRow>) => {
    setRows((prev) => (prev || []).map((r) => (keyOf(r) === keyOf(row) ? { ...r, ...fields } : r)));
  }, []);

  // The first read is the person's rows, then each sponsored agent's, one
  // call after another so a login that has to be healed is healed once and
  // never raced. Old rows stay on screen while a re-read runs, and a re-read
  // drops the answer of the read it replaces.
  React.useEffect(() => {
    if (!signedIn || !read) {
      setRows(null);
      setProblem(null);
      return;
    }
    let alive = true;
    const names = sponsoredKey ? sponsoredKey.split(",") : [];
    void (async () => {
      const got: CredentialRow[] = [];
      try {
        const own = (await selfServers("")) || [];
        got.push(...own.map((r) => ({ ...r, who: "" })));
      } catch (e) {
        // A 401 has already ended the login session inside the api module,
        // so the page redraws as signed out and says nothing here.
        const err = e as ApiError;
        if (alive && err.status !== 401) setProblem(readFailed(W.SUBJECT, err));
        return;
      }
      let failed = "";
      for (const u of names) {
        try {
          const list = (await selfServers(u)) || [];
          got.push(...list.map((r) => ({ ...r, who: u })));
        } catch (e) {
          const err = e as ApiError;
          if (err.status === 401) return;
          failed = readFailed(W.SUBJECT, err);
          got.push(...kept.current.filter((r) => r.who === u));
        }
      }
      if (!alive) return;
      setRows(got);
      setProblem(failed || null);
      setLastRead(new Date());
    })();
    return () => { alive = false; };
  }, [signedIn, read, sponsoredKey, reads]);

  // The tab's count is what waits for the person across themselves and
  // every agent they sponsor, and nothing while the list is unread.
  React.useEffect(() => {
    setCount("credentials", rows ? needing(rows) : null);
  }, [rows, setCount]);
  React.useEffect(() => () => setCount("credentials", null), [setCount]);

  // The provider's answer is taken from the address once, when the tab
  // mounts, and held until the session has settled.
  React.useEffect(() => {
    const came = takeComeBack();
    if (came) setBack(came);
  }, []);

  // A sign-in that came back is finished with the person's session, and
  // strazad stores it only when that person started it. A tab that came
  // back signed out drops the code and says so: nothing is kept to finish
  // later, and the second press at the provider is one redirect.
  React.useEffect(() => {
    if (!back || session.kind === "booting") return;
    setBack(null);
    if ("error" in back) {
      setRefusal(W.providerSaidNo(back.error));
      return;
    }
    if (session.kind !== "signed-in") {
      setNotice({ tone: "warn", text: W.CAME_BACK_SIGNED_OUT });
      return;
    }
    setFinishing(true);
    void (async () => {
      try {
        const answer = await connectFinish(back.code, back.state);
        setReads((n) => n + 1);
        notify.ok(W.signedInToast(answer.app));
      } catch (e) {
        const err = e as ApiError;
        if (err.status === 401) setNotice({ tone: "warn", text: W.CAME_BACK_SIGNED_OUT });
        else setRefusal(err.unreachable ? refused(err) : bare(err.message) + ".");
      } finally {
        setFinishing(false);
      }
    })();
  }, [back, session.kind, setNotice]);

  // signIn sends this tab to the provider. The session is renewed inside
  // the press, so the tab comes back signed in after up to five minutes
  // there, and the tab that leaves is the one that finishes.
  const signIn = async (row: CredentialRow) => {
    const key = keyOf(row);
    if (busy) return;
    setBusy(key);
    setNote(null);
    setRefusal(null);
    try {
      await refresh();
      const started = await connectStart(row.app);
      leaveFor(started.authorize_url);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setNote({ key, subject: W.SUBJECT_SIGN_IN, text: refused(err) });
    } finally {
      setBusy(null);
    }
  };

  const toggle = async (row: CredentialRow) => {
    const key = keyOf(row);
    if (busy) return;
    setBusy(key);
    setNote(null);
    const on = !row.allow_agents;
    try {
      const answer = await connectAgents(row.app, on, "");
      patch(row, { allow_agents: !!answer.allow_agents });
      notify.ok(answer.allow_agents ? W.switchedOn(row.app) : W.switchedOff(row.app, thingOf(row)));
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setNote({ key, subject: W.SUBJECT_SWITCH, text: refused(err) });
    } finally {
      setBusy(null);
    }
  };

  const saved = (row: CredentialRow, answer: ConnectAnswer) => {
    patch(row, {
      connected: true,
      fingerprint: answer.fingerprint || "",
      expires_at: answer.expires_at || "",
      set_by: answer.set_by || "",
      allow_agents: !!answer.allow_agents,
      updated_at: new Date().toISOString(),
    });
    notify.ok(W.tokenSaved(row.app, row.who));
    setSheet(null);
  };

  const removed = (done: Ask) => {
    const row = done.row;
    // A row the owner no longer reaches existed only for its credential.
    if (!row.reached) setRows((prev) => (prev || []).filter((r) => keyOf(r) !== keyOf(row)));
    else patch(row, { connected: false, fingerprint: "", expires_at: "", set_by: "", allow_agents: false, updated_at: "" });
    notify.ok(!row.reached ? W.LEFTOVER_GONE : done.kind === "disconnect" ? W.DISCONNECTED : W.TOKEN_REMOVED);
    setAsk(null);
  };

  const all = rows || [];
  const cells: Cells = {
    rows: all,
    me,
    sponsored,
    busy,
    note,
    paste: (row) => { setNote(null); setSheet(row); },
    ask: (a) => { setNote(null); setAsk(a); },
    signIn: (row) => void signIn(row),
    toggle: (row) => void toggle(row),
  };

  const cameBack = refusal && <RefusedError subject={W.SUBJECT_SIGN_IN} message={refusal} />;

  if (!signedIn) {
    return (
      <div className="flex flex-col gap-4">
        {cameBack}
        <EmptyState
          icon={KeyRoundIcon}
          title={W.SIGNED_OUT_TITLE}
          action={<Button onClick={openSignIn} data-credentials-sign-in>{SIGN_IN}</Button>}
        >
          {W.SIGNED_OUT_BODY}
        </EmptyState>
      </div>
    );
  }

  const scoped = all.filter((r) => filter === "all" || r.who === filter);
  const shown = sortRows(scoped.filter((r) => r.kind !== "none"));
  const none = [...new Set(scoped.filter((r) => r.kind === "none").map((r) => r.app))];
  const owner = filter === "all" || filter === "" ? "" : filter;
  const chips = [
    { key: "all", attr: "all", label: W.CHIP_ALL },
    { key: "", attr: "yours", label: W.chipYours(count(all, "")) },
    ...sponsored.map((u) => ({ key: u, attr: u, label: W.chipAgent(count(all, u), u) })),
  ];

  return (
    <div className="flex flex-col gap-4">
      {cameBack}
      <p className="max-w-[75ch] text-sm leading-relaxed text-text-2">{W.LEDE}</p>
      {finishing && <p className="text-sm text-muted-foreground" role="status" data-finishing>{W.FINISHING}</p>}
      {problem && <FetchError subject={W.SUBJECT} detail={problem} lastRead={lastRead} />}
      {rows === null && !problem && <p className="text-sm text-muted-foreground">{W.READING}</p>}
      {rows !== null && all.length === 0 && (
        <EmptyState icon={KeyRoundIcon} title={W.emptyTitle("")}>{W.emptyBody("")}</EmptyState>
      )}
      {rows !== null && all.length > 0 && (
        <>
          {sponsored.length > 0 && (
            <div role="group" aria-label={W.STRIP_LABEL} className="flex flex-wrap items-center gap-2" data-credentials-strip>
              {chips.map((c) => (
                <Button
                  variant="outline"
                  size="sm"
                  key={c.attr}
                  type="button"
                  aria-pressed={filter === c.key}
                  data-chip={c.attr}
                  onClick={() => setFilter(c.key)}
                  className={cn(
                    "rounded-full border px-2.5 py-1 text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                    filter === c.key ? "border-link bg-accent-bg text-link" : "border-input bg-card text-text-2",
                  )}
                >
                  {c.label}
                </Button>
              ))}
            </div>
          )}
          <CredentialTable shown={shown} scoped={scoped} none={none} owner={owner} cells={cells} />
        </>
      )}
      <PasteSheet row={sheet} onClose={() => setSheet(null)} onSaved={saved} />
      <ConfirmDialog ask={ask} onClose={() => setAsk(null)} onDone={removed} />
    </div>
  );
}

// count is how many rows of the table one chip stands for.
const count = (rows: CredentialRow[], who: string) => rows.filter((r) => r.kind !== "none" && r.who === who).length;
