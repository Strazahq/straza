import * as React from "react";
import { type ApiError, type AppRow, listApps, recheckApp, setListingCredential, setSecret } from "@/lib/api";
import { notify } from "@/lib/notify";
import { checkRow, notListed, publishRow } from "@/lib/save-words";
import { type HealthRead, healthRead, probeSay } from "@/lib/words";
import type { Problem, Row, RowState } from "./parts";

// Landed is what the commit left: the rows as they ran, the server as the
// last probe read it (null until the publish landed), that read in words,
// and the url the words name.
export type Landed = { rows: Row[]; app: AppRow | null; read: HealthRead | null; url: string };

export type InstallArgs = { name: string; url: string; secret: string | null; onSecretTried: () => void };

const SECRET_UNREACHABLE = "strazad is unreachable, so the secret may or may not be stored. Check the server's Credential tab before setting it again.";
export const CHECK_UNREACHABLE = "The check did not reach strazad. Check the connection, then Recheck.";
const RECHECK_UNREACHABLE = "The recheck did not reach strazad. Check the connection, then try again.";

const stateOf = (read: HealthRead): RowState => (read.kind === "running" ? "done" : read.kind === "refused" ? "refused" : "failed");
const bare = (message: string) => message.replace(/\.$/, "");

// problemOf is the block of a write that did not land: the server's own
// sentence, or unreachable when strazad never answered.
export function problemOf(subject: string, err: ApiError, unreachable: string): Problem {
  const said = bare(err.message);
  return { subject, unreachable: !!err.unreachable, text: err.unreachable ? unreachable : said.charAt(0).toUpperCase() + said.slice(1) + "." };
}

// findServer reads the server list for the server called name, null when
// the list does not name it or cannot be read.
export const findServer = (name: string) => listApps().then((all) => (all || []).find((a) => a.name === name) || null, () => null);

// Steps is one run of the sequence on a published server: its name, the
// url the check's words name, the secret and the listing credential to
// store or null, and the server when an earlier run found it. put reports
// each row's state as it moves, and failed each step that did not land
// with the call's error, which the list step never carries.
export type Steps = {
  name: string;
  url: string;
  secret: string | null;
  listing?: string | null;
  app: AppRow | null;
  put: (key: Row["key"], state: RowState) => void;
  failed: (step: "list" | "secret" | "listing" | "check", err?: ApiError) => void;
};

// storeAndCheck runs what follows a publish: find the server by name unless
// it is known, store the secret and the listing credential, then probe. A
// store that did not land does not stop the probe. It answers the server
// as the probe left it and that read in words, or a null server when the
// list did not name it.
export async function storeAndCheck(s: Steps): Promise<{ app: AppRow | null; read: HealthRead | null }> {
  let app = s.app;
  if (!app) {
    app = await findServer(s.name);
    if (!app) {
      s.put("check", "failed");
      s.failed("list");
      return { app: null, read: null };
    }
  }
  if (s.secret !== null) {
    s.put("secret", "running");
    try {
      await setSecret(app.id, s.secret);
      s.put("secret", "done");
    } catch (e) {
      s.put("secret", "failed");
      s.failed("secret", e as ApiError);
    }
  }
  if (s.listing != null) {
    s.put("listing", "running");
    try {
      await setListingCredential(app.id, s.listing);
      s.put("listing", "done");
    } catch (e) {
      s.put("listing", "failed");
      s.failed("listing", e as ApiError);
    }
  }
  s.put("check", "running");
  try {
    const probed = { ...app, ...(await recheckApp(app.id)) };
    const read = healthRead(probed, s.url);
    s.put("check", stateOf(read));
    return { app: probed, read };
  } catch (e) {
    s.put("check", "failed");
    s.failed("check", e as ApiError);
    return { app, read: healthRead(app, s.url) };
  }
}

// useInstall runs the rest of the wizard's commit once its draft is
// published: storeAndCheck, and the Recheck door after it. A secret never
// goes into a draft, so it is stored here, after the publish. A server
// already found is never looked up again: a second run stores the secret
// and probes. Every write the server refused or never received sets
// problem and a failure toast that stays until closed.
export function useInstall() {
  const [landed, setLanded] = React.useState<Landed | null>(null);
  const [problem, setProblem] = React.useState<Problem | null>(null);
  const [busy, setBusy] = React.useState(false);

  const refuse = (subject: string, err: ApiError, unreachable: string, lead: string) => {
    if (err.status === 401) return;
    setProblem(problemOf(subject, err, unreachable));
    notify.failed(err.unreachable ? unreachable : lead + bare(err.message) + ".");
  };

  // afterPublish answers true once the published server is found, so the
  // caller moves to the Check step; false leaves it on Review with the
  // sentence that says what to do.
  const afterPublish = async ({ name, url, secret, onSecretTried }: InstallArgs): Promise<boolean> => {
    setBusy(true);
    setProblem(null);
    let rows: Row[] = [
      { key: "install", label: publishRow(name), state: "done" },
      ...(secret !== null ? [{ key: "secret", label: "Store its secret", state: "pending" } as Row] : []),
      { key: "check", label: checkRow(name), state: "pending" },
    ];
    const put = (key: Row["key"], state: RowState) => {
      rows = rows.map((r) => (r.key === key ? { ...r, state } : r));
      setLanded((s) => ({ app: s ? s.app : null, read: s ? s.read : null, url, rows }));
    };
    const failed = (step: "list" | "secret" | "listing" | "check", err?: ApiError) => {
      if (step === "list" || !err) {
        setProblem({ subject: "Check", unreachable: false, text: notListed(name) });
        notify.failed(notListed(name));
      } else if (step === "secret") {
        refuse("Secret", err, SECRET_UNREACHABLE, "Storing the secret was refused: ");
      } else {
        refuse("Check", err, CHECK_UNREACHABLE, "The check was refused: ");
      }
    };
    try {
      const out = await storeAndCheck({ name, url, secret, app: landed && landed.app, put, failed });
      if (!out.app) return false;
      if (secret !== null) onSecretTried();
      setLanded({ rows, app: out.app, read: out.read, url });
      return true;
    } finally {
      setBusy(false);
    }
  };

  // recheck probes the landed server again and rewrites the Check row and
  // the health line from the answer.
  const recheck = async () => {
    const app = landed && landed.app;
    if (!app || busy) return;
    setBusy(true);
    setProblem(null);
    try {
      const probed = { ...app, ...(await recheckApp(app.id)) };
      const read = healthRead(probed, landed.url);
      setLanded((s) => s && { ...s, app: probed, read, rows: s.rows.map((r) => (r.key === "check" ? { ...r, state: stateOf(read) } : r)) });
      const said = probeSay({ ...probed, url: probed.url || landed.url });
      notify[said.tone](said.text);
    } catch (e) {
      refuse("Recheck", e as ApiError, RECHECK_UNREACHABLE, "The recheck was refused: ");
    } finally {
      setBusy(false);
    }
  };

  const reset = () => {
    setLanded(null);
    setProblem(null);
  };

  return { landed, problem, busy, afterPublish, recheck, reset, clearProblem: () => setProblem(null) };
}
