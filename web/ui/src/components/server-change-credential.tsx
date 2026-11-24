import type * as React from "react";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { AgentsPicker, CredentialRows, Sends, SentAs, SentName, credentialOff } from "@/components/wizard/credential-parts";
import { MISSING, headerMissing } from "@/components/wizard/form-schema";
import { CAPS, ERROR, FILL, Fold, HINT, LAB } from "@/components/wizard/parts";
import type { ManifestDoc, ProviderRow } from "@/lib/api";
import { SHARED_LINE, keepAsWritten } from "@/lib/change-words";
import { HTTP_SENT, SENT_AS, TOKEN_SENT } from "@/lib/manifest";
import { type CredentialEdit, defaultSent, readCredential } from "@/lib/manifest-edit";
import { sentWords } from "@/lib/server-words";
import { CREDENTIAL_MORE, ENV_WARNING, ENV_WARNING_SHORT, HOW_THIS_WORKS, oauthMore } from "@/lib/words";
import { type Miss, sayOf } from "./server-change-connection";

// credentialMisses lists what the Credential card cannot save without, in
// the order the card draws it.
export function credentialMisses(c: CredentialEdit): Miss[] {
  const out: Miss[] = [];
  if (c.kind === "static" || c.kind === "token") {
    if (c.sent === "header" && !c.headerName.trim()) out.push({ id: "cs-header", text: headerMissing(c.kind) });
    if (c.sent === "env" && !c.envName.trim()) out.push({ id: "cs-envname", text: MISSING.envName });
  }
  if (c.kind === "oauth" && !c.provider) out.push({ id: "cs-provider", text: MISSING.provider });
  return out;
}

type Props = { base: ManifestDoc; c: CredentialEdit; set: (patch: Partial<CredentialEdit>) => void; misses: Miss[]; providers: ProviderRow[] | null | undefined };

// CredentialFields is the Credential card's sheet: the rows of the Add
// wizard's Credential step, the picked row's fields under it, and the long
// form under a fold. The installed kind is never off. A template the
// sent-as picker cannot write stays offered as "Keep as written" while the
// kind is the installed one.
export function CredentialFields({ base, c, set, misses, providers }: Props) {
  const runtime = base.straza?.runtime?.kind || "remote";
  const http = runtime === "remote";
  const was = readCredential(base);
  const installed = base.straza?.credential;
  const provs = providers || [];
  const names = provs.map((p) => p.name);
  if (was.provider && !names.includes(was.provider)) names.unshift(was.provider);
  const prov = provs.find((p) => p.name === c.provider);
  const redirect = (prov && prov.redirect_uri) || "the redirect URI";
  const offLine = (kind: string) => (kind === was.kind ? "" : credentialOff(kind, http, providers));
  const keepable = was.sent === "keep" && c.kind === was.kind && !!installed?.inject;

  // pick moves to another card. The installed kind comes back as
  // installed. Another kind starts on a sent-as choice that fits the
  // runtime and, for a sign-in, on the first provider and its scopes.
  const pick = (kind: string) => {
    if (kind === c.kind || offLine(kind)) return;
    if (kind === was.kind) { set(was); return; }
    const patch: Partial<CredentialEdit> = { kind };
    const fits = http ? HTTP_SENT.includes(c.sent) : c.sent === "env";
    if (!fits) patch.sent = defaultSent(runtime);
    if (kind === "oauth" && !c.provider && provs[0]) { patch.provider = provs[0].name; patch.scopes = (provs[0].scopes || []).join(" "); }
    set(patch);
  };

  const sentAs = () => {
    const keys = [...(keepable ? ["keep"] : []), ...(http ? (c.kind === "token" ? TOKEN_SENT : HTTP_SENT) : ["env"])];
    const named = c.sent === "header" ? "cs-header" : c.sent === "env" ? "cs-envname" : "";
    const env = c.sent === "env";
    return (
      <>
        <div className={FILL}>
          <SentAs keys={keys} value={c.sent} onPick={(v) => set({ sent: v })}
            optionLabel={(k) => (k === "keep" && installed?.inject ? keepAsWritten(installed.inject.name || "", installed.inject.template || "") : SENT_AS[k].label)} />
          {named && (
            <SentName id={named} env={env} error={sayOf(misses, named)}
              input={{ value: env ? c.envName : c.headerName, onChange: (e) => set(env ? { envName: e.target.value } : { headerName: e.target.value }) }} />
          )}
        </div>
        {c.sent === "keep" ? <p id="cs-sent-say" className={HINT}>{sentWords(installed)}</p> : <Sends sent={c.sent} cred={c.kind} headerName={c.headerName} envName={c.envName} />}
      </>
    );
  };

  const agents = (kind: "token" | "oauth") => (
    <AgentsPicker kind={kind} value={c.agents} onPick={(v) => set({ agents: v })} lineOf={(r) => (r.value === "shared" ? SHARED_LINE[kind] : r.line)} />
  );

  let fields: React.ReactNode = null;
  if (c.kind === "static") {
    fields = (
      <>
        {sentAs()}
        {c.sent === "env" && <p className="m-0 rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-caption leading-snug text-foreground">{ENV_WARNING_SHORT}</p>}
      </>
    );
  } else if (c.kind === "token") {
    fields = <>{sentAs()}{agents("token")}</>;
  } else if (c.kind === "oauth") {
    const error = sayOf(misses, "cs-provider");
    fields = (
      <>
        <div className="flex flex-col gap-1">
          <span className={LAB}>Identity provider</span>
          <Select value={c.provider} onValueChange={(v) => { const q = provs.find((x) => x.name === v); set({ provider: v, scopes: ((q && q.scopes) || []).join(" ") }); }}>
            <SelectTrigger id="cs-provider" aria-label="identity provider" className="w-full" aria-invalid={!!error || undefined} aria-describedby="cs-provider-say"><SelectValue placeholder="Pick an identity provider" /></SelectTrigger>
            <SelectContent>{names.map((n) => <SelectItem key={n} value={n}>{n}</SelectItem>)}</SelectContent>
          </Select>
          {error
            ? <p id="cs-provider-say" className={ERROR}>{error}</p>
            : <p id="cs-provider-say" className={HINT}>Register <code className="font-mono text-foreground [overflow-wrap:anywhere]">{redirect}</code>{" at " + (c.provider || "the provider") + "."}</p>}
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor="cs-scopes" className={LAB}>Scopes</label>
          <Input id="cs-scopes" spellCheck={false} autoComplete="off" className="font-mono" aria-describedby="cs-scopes-say" value={c.scopes} onChange={(e) => set({ scopes: e.target.value })} />
          <p id="cs-scopes-say" className={HINT}>The provider's default, editable. Space-separated.</p>
        </div>
        {agents("oauth")}
      </>
    );
  }

  const more = c.kind === "oauth" ? oauthMore(c.provider, redirect) : (CREDENTIAL_MORE[c.kind] || "") + (c.kind === "static" && c.sent === "env" ? " " + ENV_WARNING : "");

  return (
    <div className="flex flex-col gap-2">
      <span className={CAPS}>What the server sees</span>
      <CredentialRows value={c.kind} onPick={pick} off={offLine} provider={c.provider || (provs[0] ? provs[0].name : "")}>
        {fields && <div className="grid gap-3.5 [&>*]:min-w-0" data-credential-box>{fields}</div>}
      </CredentialRows>
      <Fold title={HOW_THIS_WORKS}>
        <p className="m-0 max-w-[75ch] px-3.5 py-2.5 text-dense leading-relaxed text-text-2">{more}</p>
      </Fold>
    </div>
  );
}
