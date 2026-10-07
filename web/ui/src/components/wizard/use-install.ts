import * as React from "react";
import { type ApiError, type AppRow, listApps, recheckApp, setSecret } from "@/lib/api";
import { notify } from "@/lib/notify";
import { notListed, publishRow } from "@/lib/save-words";
import { type HealthRead, healthRead, probeSay } from "@/lib/words";
import type { Problem, Row, RowState } from "./parts";

// Landed is what the commit left: the rows as they ran, the server as the
// last probe read it (null until the publish landed), that read in words,
// and the url the words name.
export type Landed = { rows: Row[]; app: AppRow | null; read: HealthRead | null; url: string };

export type InstallArgs = { name: string; url: string; secret: string | null; onSecretTried: () => void };

const SECRET_UNREACHABLE = "strazad is unreachable, so the secret may or may not be stored. Check the server's Credential tab before setting it again.";
const CHECK_UNREACHABLE = "The check did not reach strazad. Check the connection, then Recheck.";
const RECHECK_UNREACHABLE = "The recheck did not reach strazad. Check the connection, then try again.";

const stateOf = (read: HealthRead): RowState => (read.kind === "running" ? "done" : read.kind === "refused" ? "refused" : "failed");
const bare = (message: string) => message.replace(/\.$/, "");

// useInstall runs the rest of the wizard's commit once its draft is
// published: find the server the publish made, store the secret, then
// probe, and the Recheck door after it. A secret never goes into a draft,
// so it is stored here, after the publish. A server already found is never
// looked up again: a second run stores the secret and probes. Every write
// the server refused or never received sets problem and a failure toast
// that stays until closed.
export function useInstall() {
  const [landed, setLanded] = React.useState<Landed | null>(null);
  const [problem, setProblem] = React.useState<Problem | null>(null);
  const [busy, setBusy] = React.useState(false);

  const refuse = (subject: string, err: ApiError, unreachable: string, lead: string) => {
    if (err.status === 401) return;
    const said = bare(err.message);
    setProblem({ subject, unreachable: !!err.unreachable, text: err.unreachable ? unreachable : said.charAt(0).toUpperCase() + said.slice(1) + "." });
    notify.failed(err.unreachable ? unreachable : lead + said + ".");
  };

  // afterPublish answers true once the published server is found, so the
  // caller moves to the Check step; false leaves it on Review with the
  // sentence that says what to do.
  const afterPublish = async ({ name, url, secret, onSecretTried }: InstallArgs): Promise<boolean> => {
    setBusy(true);
    setProblem(null);
    let app = landed && landed.app;
    let rows: Row[] = [
      { key: "install", label: publishRow(name), state: "done" },
      ...(secret !== null ? [{ key: "secret", label: "Store its secret", state: "pending" } as Row] : []),
      { key: "check", label: "Check " + name, state: "pending" },
    ];
    const put = (key: Row["key"], state: RowState) => {
      rows = rows.map((r) => (r.key === key ? { ...r, state } : r));
      setLanded((s) => ({ app: s ? s.app : null, read: s ? s.read : null, url, rows }));
    };
    try {
      if (!app) {
        app = await listApps().then((all) => (all || []).find((a) => a.name === name) || null, () => null);
        if (!app) {
          put("check", "failed");
          setProblem({ subject: "Check", unreachable: false, text: notListed(name) });
          notify.failed(notListed(name));
          return false;
        }
      }
      if (secret !== null) {
        put("secret", "running");
        try {
          await setSecret(app.id, secret);
          put("secret", "done");
        } catch (e) {
          put("secret", "failed");
          refuse("Secret", e as ApiError, SECRET_UNREACHABLE, "Storing the secret was refused: ");
        }
        onSecretTried();
      }
      put("check", "running");
      let probed = app;
      let reached = true;
      try {
        probed = { ...app, ...(await recheckApp(app.id)) };
      } catch (e) {
        reached = false;
        put("check", "failed");
        refuse("Check", e as ApiError, CHECK_UNREACHABLE, "The check was refused: ");
      }
      const read = healthRead(probed, url);
      if (reached) put("check", stateOf(read));
      setLanded({ rows, app: probed, read, url });
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
