import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { Fact } from "@/components/server-overview";
import { type ApiError, type AppRow, type Credential, type RoleRow, type SecretRow, listRoles, listSecrets, removeSecret, setSecret } from "@/lib/api";
import { notify } from "@/lib/notify";
import { KIND_NAME, sentWords } from "@/lib/server-words";
import { ENV_WARNING_SHORT, absTime, agentsLine, kindLine } from "@/lib/words";

const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";
const CHIP = "rounded bg-teal-bg px-1.5 align-[1px] font-mono text-xs text-teal";

type SecretsState = { kind: "loading" } | { kind: "ready"; rows: SecretRow[] } | { kind: "error"; err: ApiError };

const EMPTY_VALUE = "Type the secret the server expects. It is stored sealed and never shown again.";
const NO_ROLE = "Pick the application role this secret is for.";
const UNUSED = "Stored, but this credential type does not read it. Remove it, or let agents fall back to it.";
const UNUSED_ROLES = "Stored, but this credential type does not read them. Remove them, or let agents fall back to them.";
const UNUSED_REMOVE = "Nothing reads it while this credential type is set, so removing it changes no call. The stored value is deleted.";

// NOT_KNOWN is what a card says of a fact the manifest holds when the row
// carries no manifest.
export const NOT_KNOWN = "Not known here: the console cannot read a stored manifest for this server.";

// readsShared tells whether a credential reads the server's shared secret:
// kind static, or a caller kind whose agents fall back to it.
export function readsShared(cred: Credential | undefined): boolean {
  const kind = (cred && cred.kind) || "none";
  return kind === "static" || ((kind === "token" || kind === "oauth") && cred?.agents === "shared");
}

// kindMore is kindLine after its first sentence, which names the kind the
// Type row already shows by its short name.
function kindMore(cred: Credential | undefined): string {
  const line = kindLine(cred);
  const i = line.indexOf(". ");
  return i < 0 ? "" : line.slice(i + 2);
}

type FormProps = {
  app: AppRow;
  perRole: boolean;
  onDone: (role: string) => void;
  onCancel: () => void;
  onRefused: (err: ApiError) => void;
};

// SecretForm stores one sealed value: the server's shared row when perRole
// is false, a role's own row otherwise. The value never leaves the input
// except in the one POST. The primary stays clickable while a field is
// missing and says what is missing under it.
function SecretForm({ app, perRole, onDone, onCancel, onRefused }: FormProps) {
  const [value, setValue] = React.useState("");
  const [role, setRole] = React.useState("");
  const [roles, setRoles] = React.useState<RoleRow[] | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [missing, setMissing] = React.useState<"value" | "role" | null>(null);
  const input = React.useRef<HTMLInputElement>(null);
  const trigger = React.useRef<HTMLButtonElement>(null);

  React.useEffect(() => {
    if (!perRole) return;
    let alive = true;
    listRoles().then((rs) => { if (alive) setRoles(rs.filter((r) => r.kind === "application")); }, () => { if (alive) setRoles([]); });
    return () => { alive = false; };
  }, [perRole]);

  const store = async () => {
    if (perRole && !role) { setMissing("role"); trigger.current?.focus(); return; }
    if (!value) { setMissing("value"); input.current?.focus(); return; }
    setMissing(null);
    setBusy(true);
    try {
      await setSecret(app.id, value, perRole ? role : "");
      notify.ok(perRole ? "The secret for " + role + " on " + app.name + " is stored." : "The shared secret for " + app.name + " is stored.");
      onDone(perRole ? role : "");
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      onRefused(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-2 flex flex-col gap-1.5" data-secret-form>
      <div className="flex flex-wrap items-center gap-2">
        {perRole && (
          <div className="flex flex-col gap-1">
            <Select value={role} onValueChange={(v) => { setRole(v); if (missing === "role") setMissing(null); }}>
              <SelectTrigger ref={trigger} size="sm" className="min-w-56" aria-label="role for the secret" aria-invalid={missing === "role" || undefined}>
                <SelectValue placeholder="Pick an application role" />
              </SelectTrigger>
              <SelectContent>
                {(roles || []).map((r) => <SelectItem key={r.name} value={r.name}>{r.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
        )}
        <Input
          ref={input}
          type="password"
          autoComplete="off"
          spellCheck={false}
          aria-label={perRole && role ? "secret for " + role : "secret"}
          aria-invalid={missing === "value" || undefined}
          className="h-8 w-64 font-mono"
          value={value}
          onChange={(e) => { setValue(e.target.value); if (missing === "value") setMissing(null); }}
        />
        <Button size="sm" onClick={() => void store()} disabled={busy}>
          {busy && <Loader2Icon className="animate-spin" />}
          Store it
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
      </div>
      {missing && <p className="text-[13px] text-danger" role="alert">{missing === "role" ? NO_ROLE : EMPTY_VALUE}</p>}
      <p className="text-[13px] text-muted-foreground">Stored sealed. Never shown again, never returned by any read.</p>
    </div>
  );
}

// useCredentialFacts reads the server's stored secrets and returns the
// Credential card's rows, one fact each, with the doors to set or remove a
// sealed secret, and the remove confirm to render beside them. The doors
// stay on a server a file defines, since a secret never lives in the file.
export function useCredentialFacts(app: AppRow, onRefused: (err: ApiError) => void): { facts: Fact[]; overlay: React.ReactNode } {
  const [secrets, setSecrets] = React.useState<SecretsState>({ kind: "loading" });
  const [nonce, setNonce] = React.useState(0);
  const [form, setForm] = React.useState<"app" | "role" | null>(null);
  const [ask, setAsk] = React.useState<SecretRow | null>(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    listSecrets(app.id).then(
      (rows) => { if (alive) setSecrets({ kind: "ready", rows: Array.isArray(rows) ? rows : [] }); },
      (e) => { if (alive && (e as ApiError).status !== 401) setSecrets({ kind: "error", err: e as ApiError }); },
    );
    return () => { alive = false; };
  }, [app.id, nonce]);

  const reload = () => setNonce((n) => n + 1);

  const remove = async (row: SecretRow) => {
    setBusy(true);
    try {
      await removeSecret(app.id, row.role || "");
      notify.ok(row.role ? "The secret for " + row.role + " on " + app.name + " was removed." : "The shared secret for " + app.name + " was removed.");
      reload();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      onRefused(err);
    } finally {
      setBusy(false);
      setAsk(null);
    }
  };

  const cred = app.manifest?.straza?.credential;
  // An older server sends no manifest, so the kind is not known here; the
  // stored rows are what strazad holds, and the doors stay open the way the
  // old page kept them.
  const manifestKnown = !!app.manifest;
  const kind = cred?.kind || "none";
  const caller = kind === "token" || kind === "oauth";
  const what = kind === "token" ? "token" : "sign-in";
  const sharedRow = !manifestKnown || readsShared(cred);
  const rows = secrets.kind === "ready" ? secrets.rows : [];
  const own = rows.find((r) => r.scope === "app" || !r.role);
  const perRole = rows.filter((r) => r.role);
  const doorsOpen = secrets.kind === "ready";

  const stored = (() => {
    if (secrets.kind === "loading") return <span className="text-muted-foreground">Reading the stored credential.</span>;
    if (secrets.kind === "error") return "The stored credential could not be read: " + (secrets.err.unreachable ? "strazad did not answer" : secrets.err.message) + ". Reload the page to try again.";
    if (own) {
      return (
        <>
          {"Set " + absTime(own.set_at) + ", fingerprint "}<code className={CODE}>{own.fingerprint + "…"}</code>{". Never shown again. "}
          <Button size="sm" variant="ghost" className="h-7" onClick={() => setForm("app")}>Set it again</Button>
          <Button size="sm" variant="ghost" className="h-7" onClick={() => setAsk(own)}>Remove</Button>
        </>
      );
    }
    if (sharedRow) {
      return (
        <>
          {"No shared secret is stored yet. "}
          <Button size="sm" variant="ghost" className="h-7" onClick={() => setForm("app")}>Set one</Button>
        </>
      );
    }
    if (caller) return "Nothing here. Each person's " + what + " is sealed under their own id.";
    return "Nothing.";
  })();

  const facts: Fact[] = [];
  facts.push(manifestKnown
    ? { label: "Type", body: <>{(KIND_NAME[kind] || kind) + " "}<code className={CHIP}>{kind}</code></>, sub: kindMore(cred) }
    : { label: "Type", body: NOT_KNOWN + " The rows below are what strazad holds." });
  if (kind === "oauth") {
    const scopes = cred?.oauth?.scopes || [];
    facts.push({ label: "Provider and scopes", body: <>{cred?.oauth?.provider || "the provider"}{scopes.length ? <>{", scopes "}<code className={CODE}>{scopes.join(" ")}</code></> : ", no scopes named"}</> });
  }
  if (manifestKnown && kind !== "none") facts.push({ label: "Sent as", body: sentWords(cred) });
  if (caller) facts.push({ label: "Agents with nothing of their own", body: agentsLine(cred) });
  facts.push({
    label: "Stored here",
    body: <>{stored}{form === "app" && doorsOpen && <SecretForm app={app} perRole={false} onDone={() => { setForm(null); reload(); }} onCancel={() => setForm(null)} onRefused={onRefused} />}</>,
    warn: own && !sharedRow ? UNUSED : undefined,
  });
  // Every stored role row shows whatever the kind is, since a row the kind
  // does not read today applies again when agents fall back to the shared
  // secret. The door to add one stays where a role secret is read.
  if (sharedRow || perRole.length > 0 || caller) {
    facts.push({
      label: "Per-role secrets",
      body: (
        <div data-role-secrets>
          {doorsOpen && perRole.length === 0 && (sharedRow ? "None. " : "Not used here. Each caller's own " + what + " is the credential.")}
          {perRole.map((r) => (
            <div key={r.id} className="flex flex-wrap items-center gap-x-1">
              <span>{r.role + ", fingerprint "}<code className={CODE}>{r.fingerprint + "…"}</code>{", set " + absTime(r.set_at)}</span>
              <Button size="sm" variant="ghost" className="h-7" onClick={() => setAsk(r)}>Remove</Button>
            </div>
          ))}
          {doorsOpen && sharedRow && (
            <>
              <Button size="sm" variant="ghost" className="h-7" onClick={() => setForm("role")}>Add one for a role</Button>
              {" A role's own secret replaces the shared one for that role's " + (caller ? "agents" : "calls") + "."}
            </>
          )}
          {form === "role" && doorsOpen && sharedRow && <SecretForm app={app} perRole onDone={() => { setForm(null); reload(); }} onCancel={() => setForm(null)} onRefused={onRefused} />}
        </div>
      ),
      warn: !sharedRow && perRole.length > 0 ? UNUSED_ROLES : undefined,
    });
  }
  if (kind !== "none" && cred?.inject?.as === "env") facts.push({ label: "Watch out", body: ENV_WARNING_SHORT });

  const overlay = (
    <AlertDialog open={ask !== null} onOpenChange={(open) => { if (!open) setAsk(null); }}>
      <AlertDialogContent className="sm:max-w-[560px]">
        <AlertDialogHeader>
          <AlertDialogTitle>{ask?.role ? "Remove the secret for " + ask.role + "?" : "Remove the shared secret?"}</AlertDialogTitle>
          <AlertDialogDescription>
            {!sharedRow
              ? UNUSED_REMOVE
              : ask?.role
                ? "Calls by " + ask.role + " fall back to the shared secret, or run without one when none is stored."
                : "Calls and health checks for " + app.name + " run without a credential from now on, unless a role has its own. A server that requires one reads degraded until a secret is set again."}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" disabled={busy} onClick={() => { if (ask) void remove(ask); }}>Remove secret</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  return { facts, overlay };
}
