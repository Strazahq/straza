import type * as React from "react";
import { Controller, type Control, type FieldErrors } from "react-hook-form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { ProviderRow } from "@/lib/api";
import { type Form, HTTP_SENT, SENT_AS, TOKEN_SENT, sentFor } from "@/lib/manifest";
import { AGENT_ROWS, CALLER_OFF_SHORT, CREDENTIAL_MORE, ENV_WARNING, ENV_WARNING_SHORT, credentialCards, oauthMore, sentShort } from "@/lib/words";
import { CAPS, CardGroup, ERROR, Field, Fold, HINT, ManifestFold, OptionCard, TWO } from "./parts";
import { DryLine } from "./server-step";
import type { Dry } from "./use-dry-run";
import type { FieldFn, SetFn } from "./use-wizard-form";

const NO_PROVIDER = "No identity provider is configured on this server, so nobody can sign in through one yet. Add oauth.providers.<name> to the strazad config first, or use one shared secret.";

type Props = {
  f: Form;
  errors: FieldErrors<Form>;
  control: Control<Form>;
  field: FieldFn;
  set: SetFn;
  providers: ProviderRow[] | null | undefined;
  installed: string;
  dry: Dry;
  yaml: string;
};

const say = (id: string, error?: string, hint?: React.ReactNode) =>
  error ? <p id={id} className={ERROR}>{error}</p> : hint ? <p id={id} className={HINT}>{hint}</p> : null;

// CredentialStep asks whose credential Straza adds to each call: four
// cards with one line each, the chosen card's fields in one box below
// them, the long form of the choice under a fold, and the manifest fold.
// installed names the server once it exists, so the step says a second
// store replaces the sealed value.
export function CredentialStep({ f, errors, control, field, set, providers, installed, dry, yaml }: Props) {
  const http = f.runtime === "remote";
  const sent = sentFor(f);
  const provs = providers || [];
  const prov = provs.find((p) => p.name === f.provider) || provs[0];
  const redirect = (prov && prov.redirect_uri) || "the redirect URI";
  const offLine = (kind: string) => (kind !== "token" && kind !== "oauth" ? "" : !http ? CALLER_OFF_SHORT : kind === "oauth" && Array.isArray(providers) && providers.length === 0 ? NO_PROVIDER : "");
  const cards = credentialCards(f.provider || (prov ? prov.name : ""));
  const chosen = cards.find((c) => c.kind === f.cred) || cards[0];

  const pick = (kind: string) => {
    if (kind === f.cred || offLine(kind)) return;
    // A named header with no name yet is a manifest the server refuses, so
    // a secret or token on an HTTP server starts as a bearer.
    if (kind === "token" || kind === "static") { set({ cred: kind, sentAs: sent === "header" && !f.headerName.trim() ? "bearer" : f.sentAs }); return; }
    if (kind === "oauth") {
      const keep = !!prov && prov.name === f.provider && !!f.scopes.trim();
      set({ cred: "oauth", agents: f.agents === "own" ? "sponsor" : f.agents, provider: prov ? prov.name : "", scopes: keep ? f.scopes : ((prov && prov.scopes) || []).join(" ") });
      return;
    }
    set({ cred: kind });
  };

  const secretBox = (hint: string) => (
    <Field id="mw-secret" label="secret" error={errors.secret?.message} hint={hint}>
      <Input id="mw-secret" type="password" autoComplete="off" spellCheck={false} aria-label="secret" className="font-mono"
        aria-invalid={!!errors.secret || undefined} aria-describedby="mw-secret-say" {...field("secret")} />
    </Field>
  );

  const sentAs = (keys: string[]) => {
    const env = sent === "env";
    const named = env ? "envName" : "headerName";
    const error = (sent === "header" || env) ? errors[named]?.message : undefined;
    return (
      <div className="flex flex-col gap-1">
        <span className={CAPS}>sent as</span>
        <Select value={sent} onValueChange={(v) => set({ sentAs: v })}>
          <SelectTrigger aria-label="sent as" className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>{keys.map((k) => <SelectItem key={k} value={k}>{SENT_AS[k].label}</SelectItem>)}</SelectContent>
        </Select>
        {(sent === "header" || env) && (
          <Input aria-label={env ? "variable name" : "header name"} placeholder={env ? "API_TOKEN" : "X-Api-Key"} spellCheck={false} className="font-mono"
            aria-invalid={!!error || undefined} aria-describedby="mw-sent-say" {...field(named)} />
        )}
        {say("mw-sent-say", error, sentShort(sent, f.cred, f.headerName))}
      </div>
    );
  };

  const agents = (kind: "token" | "oauth") => (
    <div className="flex flex-col gap-1.5">
      <span className={CAPS}>{"agents with no " + (kind === "token" ? "token" : "sign-in") + " of their own"}</span>
      <CardGroup label="agents" value={f.agents} onPick={(v) => set({ agents: v })} className="gap-1.5">
        {AGENT_ROWS[kind].map((r) => (
          <OptionCard key={r.value} row value={r.value} label={"agents " + r.value} name={r.name} tag={"agents: " + r.value} line={r.line} on={f.agents === r.value} />
        ))}
      </CardGroup>
    </div>
  );

  let fields: React.ReactNode = null;
  if (f.cred === "static") {
    fields = (
      <>
        <div className={TWO}>{secretBox("Stored sealed. Never shown again.")}{sentAs(http ? HTTP_SENT : ["env"])}</div>
        {sent === "env" && <p className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-snug text-foreground">{ENV_WARNING_SHORT}</p>}
      </>
    );
  } else if (f.cred === "token") {
    fields = (
      <>
        <div className={TWO}>
          {sentAs(TOKEN_SENT)}
          <div className="flex flex-col gap-1">
            <span className={CAPS}>what the wizard stores</span>
            <p className={HINT}>Nothing yet. People paste their own token on the Credentials tab of their self-service page after install.</p>
          </div>
        </div>
        {agents("token")}
        {f.agents === "shared" && secretBox("Stored sealed after install. Only agents use it.")}
      </>
    );
  } else if (f.cred === "oauth") {
    fields = (
      <>
        <div className={TWO}>
          <div className="flex flex-col gap-1">
            <span className={CAPS}>provider</span>
            <Controller control={control} name="provider" render={({ field: p }) => (
              <Select value={p.value} onValueChange={(v) => { p.onChange(v); const q = provs.find((x) => x.name === v); set({ scopes: ((q && q.scopes) || []).join(" ") }); }}>
                <SelectTrigger ref={p.ref} aria-label="provider" className="w-full" aria-invalid={!!errors.provider || undefined} aria-describedby="mw-provider-say"><SelectValue /></SelectTrigger>
                <SelectContent>{provs.map((q) => <SelectItem key={q.name} value={q.name}>{q.name}</SelectItem>)}</SelectContent>
              </Select>
            )} />
            {say("mw-provider-say", errors.provider?.message, <>Register <code className="font-mono text-foreground">{redirect}</code>{" at " + (f.provider || "the provider") + "."}</>)}
          </div>
          <Field id="mw-scopes" label="scopes" hint="The provider's default, editable. Space-separated.">
            <Input id="mw-scopes" aria-label="scopes" spellCheck={false} className="font-mono" aria-describedby="mw-scopes-say" {...field("scopes")} />
          </Field>
        </div>
        {agents("oauth")}
      </>
    );
  }

  const caller = f.cred === "token" || f.cred === "oauth";
  const more = f.cred === "oauth" ? oauthMore(f.provider, redirect) : (CREDENTIAL_MORE[f.cred] || "") + (f.cred === "static" && sent === "env" ? " " + ENV_WARNING : "");

  return (
    <div className="flex flex-col gap-4">
      {installed && <p className={HINT}>{installed + " is installed. Storing the secret again replaces the sealed value, then the check runs under it."}</p>}
      <CardGroup label="credential" value={f.cred} onPick={pick} className="grid-cols-2">
        {cards.map((c) => (
          <OptionCard key={c.kind} value={c.kind} label={"credential " + c.kind} name={c.name} tag={"kind: " + c.kind} line={offLine(c.kind) || c.line}
            on={f.cred === c.kind} off={!!offLine(c.kind)} />
        ))}
      </CardGroup>
      {fields
        ? <div className="flex flex-col gap-3.5 rounded-md border border-border bg-card px-4 py-3" data-credential-box><div className="font-semibold text-foreground">{chosen.name}</div>{fields}</div>
        : <p className={HINT}>Nothing to set for this choice.</p>}
      {dry.state === "refused" && <DryLine dry={dry} />}
      <Fold title="More about this choice">
        <div className="flex max-w-[80ch] flex-col gap-2 px-3.5 py-2.5 text-sm leading-relaxed text-text-2">
          <p>{more}</p>
          {caller && (
            <ul className="m-0 flex list-none flex-col gap-1 p-0">
              {AGENT_ROWS[f.cred].map((r) => <li key={r.value}><b className="font-semibold text-foreground">{r.name}</b>{": " + r.more}</li>)}
            </ul>
          )}
        </div>
      </Fold>
      <ManifestFold yaml={yaml} />
    </div>
  );
}
