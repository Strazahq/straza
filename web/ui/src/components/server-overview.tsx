import * as React from "react";
import { Button } from "@/components/ui/button";
import { HelpTip } from "@/components/help-tip";
import type { ChangeWhich } from "@/components/server-change-sheet";
import { OriginLine } from "@/components/origin-line";
import { NOT_KNOWN, useCredentialFacts } from "@/components/server-credential";
import { RoleChip } from "@/components/status-badge";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type AppRow, type Credential, listUsers, query } from "@/lib/api";
import { notify } from "@/lib/notify";
import { joinArgs } from "@/lib/manifest-edit";
import {
  ABOVE_IT, ACCESS_CHECK, ADMIN_ROLE_HELP, ADMIN_ROLE_HINT, CARD, CHECKS_LABEL, HOLDERS_NEXT, HOLDERS_NOT_READ, HOLDERS_NOT_READABLE, NOBODY_HOLDS, OTHER_ADMIN_ROLES, POLICY_CHECK,
  adminRoleSub, exposeWords, rpsWords, timeoutWords, transportWords,
} from "@/lib/server-words";

// CODE is the mono chip for an identifier, a path or a command in a row.
export const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";
const DT = "pt-0.5 text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
const DD = "m-0 min-w-0 text-sm leading-relaxed text-text-2 [overflow-wrap:anywhere]";

// Fact is one labeled row of a card: the label, the value, and optional
// lines under the value, muted (sub) or in the warn hue (warn).
export type Fact = { label: string; body: React.ReactNode; sub?: string; warn?: string };

// Facts renders labeled rows, the label column left and the value right.
export function Facts({ facts }: { facts: Fact[] }) {
  return (
    <dl className="m-0 grid grid-cols-1 gap-x-5 gap-y-2.5 sm:grid-cols-[200px_minmax(0,1fr)]">
      {facts.map((f) => (
        <React.Fragment key={f.label}>
          <dt className={DT}>{f.label}</dt>
          <dd className={DD}>
            {f.body}
            {f.sub && <span className="block text-[13px] text-muted-foreground" data-sub>{f.sub}</span>}
            {f.warn && <span className="block text-[13px] text-warn" data-warn>{f.warn}</span>}
          </dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

// Address is a remote server's address as a code chip with a Copy button;
// the toast says whether the clipboard took it.
export function Address({ url }: { url: string }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      notify.ok("Copied the address.");
    } catch {
      notify.failed("The address could not be copied: the browser refused the clipboard. Select it and copy it by hand.");
    }
  };
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <code className={CODE}>{url}</code>
      <Button variant="outline" size="sm" aria-label="Copy the address" onClick={() => void copy()}>Copy</Button>
    </span>
  );
}

type EnvVar = { name?: string; value?: string };

// envBody lists a command's environment as name=value, then the variable
// the credential is sent in, whose value is the sealed secret.
function envBody(env: unknown[] | undefined, cred: Credential | undefined): React.ReactNode {
  const vars = (env || []) as EnvVar[];
  const sealed = cred && cred.kind && cred.kind !== "none" && cred.inject?.as === "env" ? cred.inject.name || "" : "";
  if (vars.length === 0 && !sealed) return "None.";
  return (
    <span className="flex flex-col items-start gap-1">
      {vars.map((v, i) => <code key={i} className={CODE}>{(v.name || "") + "=" + (v.value || "")}</code>)}
      {sealed && <span><code className={CODE}>{sealed}</code>{" from the stored secret, sealed."}</span>}
    </span>
  );
}

// connectionFacts is how Straza reaches the server: the transport, what
// the runtime runs or where it answers, and its version.
function connectionFacts(app: AppRow): Fact[] {
  const rt = app.manifest?.straza?.runtime;
  const kind = rt?.kind || app.runtime;
  const facts: Fact[] = [{ label: "Transport", body: transportWords(kind) }];
  const url = rt?.remote?.url || app.url;
  if (kind === "remote" && url) facts.push({ label: "Address", body: <Address url={url} /> });
  const cmd = rt?.command;
  if (kind === "command" && cmd) {
    facts.push({ label: "Executable", body: <code className={CODE}>{cmd.exec || ""}</code> });
    facts.push({ label: "Arguments", body: cmd.args && cmd.args.length ? <code className={CODE}>{joinArgs(cmd.args)}</code> : "None." });
    facts.push({ label: "Working directory", body: cmd.workdir ? <code className={CODE}>{cmd.workdir}</code> : "Not set, so strazad's own." });
    facts.push({ label: "Environment", body: envBody(cmd.env, app.manifest?.straza?.credential) });
  }
  if (kind === "oci" && rt?.oci?.image) facts.push({ label: "Image", body: <code className={CODE}>{rt.oci.image}</code> });
  facts.push({ label: "Version", body: app.version || "Not recorded.", sub: "The server's own version, from its registry record." });
  if (kind === "remote" && rt?.remote?.auth === "passthrough") facts.push({ label: "Authentication", body: "Straza adds no credential: auth passthrough is set in its manifest." });
  return facts;
}

// settingsFacts is what Straza lets through: the description, the tools it
// exposes, the rate limit and the per-call timeout. A cpu or mem limit in
// the manifest is not shown, since no runtime applies it.
function settingsFacts(app: AppRow, upstreamTimeout: number | null): Fact[] {
  if (!app.manifest) return [{ label: "Manifest", body: NOT_KNOWN }];
  const s = app.manifest.straza;
  return [
    { label: "Description", body: app.manifest.metadata?.description || "None." },
    { label: "Tools exposed", body: exposeWords(s?.exposure?.tools) },
    { label: "Rate limit", body: rpsWords(s?.limits?.rps) },
    { label: "Per-call timeout", body: timeoutWords(s?.limits?.timeoutSeconds, upstreamTimeout) },
  ];
}

type Holders = { kind: "reading" } | { kind: "ready"; names: string[] } | { kind: "closed" } | { kind: "failed" };

// useHolders reads who holds the server's admin role, one page of a
// hundred the way the role page's Holders tab reads it. A session that may
// not list users, a server admin among them, gets the closed word.
function useHolders(role: string | undefined): Holders {
  const [state, setState] = React.useState<Holders>({ kind: "reading" });
  React.useEffect(() => {
    let alive = true;
    if (!role) { setState({ kind: "failed" }); return; }
    setState({ kind: "reading" });
    listUsers(query({ role, sort: "name", order: "asc", limit: 100 })).then(
      (page) => { if (alive) setState({ kind: "ready", names: page.items.map((u) => u.username) }); },
      (e) => { if (alive) setState({ kind: (e as ApiError).status === 403 ? "closed" : "failed" }); },
    );
    return () => { alive = false; };
  }, [role]);
  return state;
}

// adminRoleFacts is who may change the server: the minted role with its
// badge and help, its holders, and the two roles above every server.
function adminRoleFacts(app: AppRow, holders: Holders): Fact[] {
  const role = app.admin_role;
  const count = holders.kind === "ready" ? holders.names.length : null;
  const roleBody = role
    ? <span className="inline-flex flex-wrap items-center gap-1.5"><RoleChip name={role} /><WordBadge word="server admin role" tone="teal" attr="data-minted" /><HelpTip label="admin role" text={ADMIN_ROLE_HELP} /></span>
    : NOT_KNOWN;
  const holdersBody = holders.kind === "ready"
    ? holders.names.length ? holders.names.map((n) => <code key={n} className={CODE + " mr-1"}>{n}</code>) : NOBODY_HOLDS
    : holders.kind === "closed" ? HOLDERS_NOT_READABLE : holders.kind === "failed" ? HOLDERS_NOT_READ : "Reading.";
  return [
    { label: "Admin role", body: roleBody, sub: role ? adminRoleSub(count) : undefined },
    { label: "Holders", body: holdersBody, sub: holders.kind === "ready" && holders.names.length ? HOLDERS_NEXT : undefined },
    { label: OTHER_ADMIN_ROLES, body: ABOVE_IT },
  ];
}

type CardProps = { title: string; hint: string; which: ChangeWhich | "admin-role"; onChange: ((which: ChangeWhich) => void) | null; children: React.ReactNode };

// Card is one Overview widget: its header with the title, the hint and the
// Change button when the console may change the server, then its rows.
function Card({ title, hint, which, onChange, children }: CardProps) {
  const id = React.useId();
  return (
    <section aria-labelledby={id} className="rounded-md border border-border bg-card" data-card={which}>
      <header className="flex items-center gap-2 border-b border-border px-4 py-3">
        <div className="min-w-0"><h2 id={id} className="text-sm font-semibold text-foreground">{title}</h2>
        <p className="mt-1 text-[13px] text-text-2">{hint}</p></div>
        {onChange && which !== "admin-role" && <Button variant="outline" size="sm" className="ml-auto" aria-label={"Change the " + which} onClick={() => onChange(which)}>Change</Button>}
      </header>
      <div className="px-4 py-3">{children}</div>
    </section>
  );
}

type Props = {
  app: AppRow;
  upstreamTimeout: number | null;
  onChange: (which: ChangeWhich) => void;
  onRefused: (err: ApiError) => void;
  onInspectAccess?: () => void;
  onTest?: () => void;
};

// ServerOverview is the Overview tab of a server's page: the origin line,
// then the Connection, Credential, Settings and Administered by cards.
// Every card with a stored manifest carries Change, a server a file names
// included, since the file proposes drafts and owns nothing. A row with no
// stored manifest has no Change, because a sheet
// would have nothing to edit. Administered by is read-only for everyone,
// because the role is set at the mint and goes with the server.
export function ServerOverview({ app, upstreamTimeout, onChange, onRefused, onInspectAccess, onTest }: Props) {
  const credential = useCredentialFacts(app, onRefused);
  const holders = useHolders(app.admin_role);
  const change = !app.manifest ? null : onChange;
  return (
    <div className="server-overview" data-server-overview>
      <OriginLine object={"App/" + app.name} name={app.name} file={app.file} differs={app.file_differs} />
      <div className="server-overview-cards">
      <Card title={CARD.connection.title} hint={CARD.connection.hint} which="connection" onChange={change}>
        <Facts facts={connectionFacts(app)} />
      </Card>
      <Card title={CARD.credential.title} hint={CARD.credential.hint} which="credential" onChange={change}>
        <Facts facts={credential.facts} />
        {credential.overlay}
      </Card>
      <section className="server-access-guide" aria-label={CHECKS_LABEL}>
        <div><h2>{ACCESS_CHECK.title}</h2><p>{ACCESS_CHECK.line}</p>{onInspectAccess && <Button variant="outline" size="sm" onClick={onInspectAccess}>{ACCESS_CHECK.action}</Button>}</div>
        <div><h2>{POLICY_CHECK.title}</h2><p>{POLICY_CHECK.line}</p>{onTest && <Button variant="outline" size="sm" onClick={onTest}>{POLICY_CHECK.action}</Button>}</div>
      </section>
      <Card title={CARD.settings.title} hint={CARD.settings.hint} which="settings" onChange={change}>
        <Facts facts={settingsFacts(app, upstreamTimeout)} />
      </Card>
      <Card title={CARD.admin.title} hint={CARD.admin.hint} which="admin-role" onChange={null}>
        <Facts facts={adminRoleFacts(app, holders)} />
        <p className="mt-2.5 text-[13px] text-muted-foreground" data-admin-hint>{ADMIN_ROLE_HINT}</p>
      </Card>
      </div>
    </div>
  );
}
