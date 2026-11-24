import type * as React from "react";
import { Controller, type Control, type FieldErrors } from "react-hook-form";
import { Pill } from "@/components/chip";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ManifestFold } from "@/components/yaml-fold";
import type { ProviderRow } from "@/lib/api";
import { type Form, HTTP_SENT, TOKEN_SENT, injectFor, sentFor } from "@/lib/manifest";
import {
  AGENTS_SECRET_HINT, AGENT_ROWS, CHANGE_ON_PAGE, CREDENTIAL_CHOICES, CREDENTIAL_MORE, ENV_WARNING, ENV_WARNING_SHORT, HOW_THIS_WORKS, SECRET_HINT, STORED_NOW,
  STORE_AGAIN_LINE, injectPill, oauthMore,
} from "@/lib/words";
import { AgentsPicker, CredentialRows, Sends, SentAs, SentName, credentialOff } from "./credential-parts";
import { ERROR, FILL, Fold, HINT, LAB } from "./parts";
import { DryLine } from "./server-step";
import type { Dry } from "./use-dry-run";
import type { FieldFn, SetFn } from "./use-wizard-form";

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

// CredentialStep asks whose credential Straza adds to each call: one row
// per choice with its fact as a pill, the picked row's fields under it,
// the long form of the choice under a fold, and the manifest fold.
// installed names the server once it exists. The installed choice then
// stands as facts, and the step asks only for the secret it stores again.
export function CredentialStep({ f, errors, control, field, set, providers, installed, dry, yaml }: Props) {
  const http = f.runtime === "remote";
  const sent = sentFor(f);
  const provs = providers || [];
  const prov = provs.find((p) => p.name === f.provider) || provs[0];
  const redirect = (prov && prov.redirect_uri) || "the redirect URI";
  const offLine = (kind: string) => credentialOff(kind, http, providers);

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

  const secretBox = (label: string, hint: string) => (
    <div className="flex flex-col gap-1">
      <label htmlFor="mw-secret" className={LAB}>{label}</label>
      <Input id="mw-secret" type="password" autoComplete="off" spellCheck={false} aria-label="secret" className="font-mono"
        aria-invalid={!!errors.secret || undefined} aria-describedby="mw-secret-say" {...field("secret")} />
      {say("mw-secret-say", errors.secret?.message, hint)}
    </div>
  );
  const agentsSecret = () => secretBox("Secret for agents", AGENTS_SECRET_HINT);
  const sends = <Sends sent={sent} cred={f.cred} headerName={f.headerName} envName={f.envName} />;

  // The installed choice stands as facts. A change of choice goes through
  // the server's page, since storing the secret again publishes nothing.
  if (installed) {
    const choice = CREDENTIAL_CHOICES.find((c) => c.kind === f.cred) || CREDENTIAL_CHOICES[0];
    const inject = injectFor(f);
    return (
      <div className="flex flex-col gap-4">
        <p className={HINT}>{STORE_AGAIN_LINE}</p>
        <div className="flex flex-col gap-1.5">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 rounded-md border border-border bg-card px-3 py-2 text-dense" data-installed-choice>
            <span className="font-semibold text-foreground">{choice.label}</span>
            <Pill>{choice.fact}</Pill>
            <Pill>{injectPill(inject.as, inject.name)}</Pill>
          </div>
          <p className={HINT}>{CHANGE_ON_PAGE}</p>
        </div>
        <div className="flex flex-col gap-3.5 rounded-md border border-border bg-background px-4 py-3.5" data-credential-box>
          <div className={FILL}>{f.cred === "token" ? agentsSecret() : secretBox("Secret", SECRET_HINT)}</div>
          {sends}
        </div>
        <ManifestFold yaml={yaml} />
      </div>
    );
  }

  // sentAs is the Sent as picker, the name a header or a variable needs,
  // both in the grid of the picked row's fields.
  const sentAs = (keys: string[]) => {
    const env = sent === "env";
    const named = env ? "envName" : "headerName";
    return (
      <>
        <SentAs keys={keys} value={sent} onPick={(v) => set({ sentAs: v })} />
        {(sent === "header" || env) && <SentName id={env ? "mw-envname" : "mw-header"} env={env} error={errors[named]?.message} input={field(named)} />}
      </>
    );
  };

  let fields: React.ReactNode = null;
  if (f.cred === "static") {
    fields = (
      <>
        <div className={FILL}>{secretBox("Secret", SECRET_HINT)}{sentAs(http ? HTTP_SENT : ["env"])}</div>
        {sends}
        {sent === "env" && <p className="m-0 rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-caption leading-snug text-foreground">{ENV_WARNING_SHORT}</p>}
      </>
    );
  } else if (f.cred === "token") {
    fields = (
      <>
        <div className={FILL}>{sentAs(TOKEN_SENT)}</div>
        {sends}
        <div className="flex flex-col gap-1">
          <span className={LAB}>{STORED_NOW.label}</span>
          <div className="flex flex-wrap gap-2">{STORED_NOW.pills.map((p) => <Pill key={p} className="max-w-full max-sm:whitespace-normal">{p}</Pill>)}</div>
        </div>
        <AgentsPicker kind="token" value={f.agents} onPick={(v) => set({ agents: v })} />
        {f.agents === "shared" && agentsSecret()}
      </>
    );
  } else if (f.cred === "oauth") {
    fields = (
      <>
        <div className={FILL}>
          <div className="flex flex-col gap-1">
            <span className={LAB}>Provider</span>
            <Controller control={control} name="provider" render={({ field: p }) => (
              <Select value={p.value} onValueChange={(v) => { p.onChange(v); const q = provs.find((x) => x.name === v); set({ scopes: ((q && q.scopes) || []).join(" ") }); }}>
                <SelectTrigger ref={p.ref} aria-label="provider" className="w-full" aria-invalid={!!errors.provider || undefined} aria-describedby="mw-provider-say"><SelectValue /></SelectTrigger>
                <SelectContent>{provs.map((q) => <SelectItem key={q.name} value={q.name}>{q.name}</SelectItem>)}</SelectContent>
              </Select>
            )} />
            {say("mw-provider-say", errors.provider?.message, <>Register <code className="font-mono text-foreground [overflow-wrap:anywhere]">{redirect}</code>{" at " + (f.provider || "the provider") + "."}</>)}
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="mw-scopes" className={LAB}>Scopes</label>
            <Input id="mw-scopes" aria-label="scopes" spellCheck={false} className="font-mono" aria-describedby="mw-scopes-say" {...field("scopes")} />
            <p id="mw-scopes-say" className={HINT}>The provider's default, editable. Space-separated.</p>
          </div>
        </div>
        <AgentsPicker kind="oauth" value={f.agents} onPick={(v) => set({ agents: v })} />
      </>
    );
  }

  const caller = f.cred === "token" || f.cred === "oauth";
  const more = f.cred === "oauth" ? oauthMore(f.provider, redirect) : (CREDENTIAL_MORE[f.cred] || "") + (f.cred === "static" && sent === "env" ? " " + ENV_WARNING : "");

  return (
    <div className="flex flex-col gap-4">
      <CredentialRows value={f.cred} onPick={pick} off={offLine} provider={f.provider || (prov ? prov.name : "")}>
        {fields && <div className="grid gap-3.5 [&>*]:min-w-0" data-credential-box>{fields}</div>}
      </CredentialRows>
      {dry.state === "refused" && <DryLine dry={dry} />}
      <Fold title={HOW_THIS_WORKS}>
        <div className="flex max-w-[80ch] flex-col gap-2 px-3.5 py-2.5 text-dense leading-relaxed text-text-2">
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
