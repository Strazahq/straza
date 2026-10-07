import type * as React from "react";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { MISSING, headerMissing } from "@/components/wizard/form-schema";
import { CAPS, CardGroup, ERROR, Field, Fold, HINT, OptionCard } from "@/components/wizard/parts";
import type { ManifestDoc, ProviderRow } from "@/lib/api";
import { NO_PROVIDER, SHARED_LINE, keepAsWritten } from "@/lib/change-words";
import { HTTP_SENT, SENT_AS, TOKEN_SENT } from "@/lib/manifest";
import { type CredentialEdit, callerKind, defaultSent, readCredential } from "@/lib/manifest-edit";
import { sentWords } from "@/lib/server-words";
import { AGENT_ROWS, CALLER_OFF_SHORT, CREDENTIAL_MORE, ENV_WARNING, ENV_WARNING_SHORT, credentialCards, oauthMore, sentShort } from "@/lib/words";
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

// CredentialFields is the Credential card's sheet: the four cards one
// line each, the chosen card's fields in one box, and the long form under
// a fold. A template the sent-as picker cannot write stays offered as
// "Keep as written" while the kind is the installed one.
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
  const offLine = (kind: string) => (!callerKind(kind) || kind === was.kind ? "" : !http ? CALLER_OFF_SHORT : kind === "oauth" && Array.isArray(providers) && providers.length === 0 ? NO_PROVIDER : "");
  const cards = credentialCards(c.provider || (provs[0] ? provs[0].name : ""));
  const chosen = cards.find((k) => k.kind === c.kind) || cards[0];
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
    const error = named ? sayOf(misses, named) : undefined;
    const hint = c.sent === "keep" ? sentWords(installed) : sentShort(c.sent, c.kind, c.headerName);
    return (
      <div className="flex flex-col gap-1">
        <span className={CAPS}>Sent as</span>
        <Select value={c.sent} onValueChange={(v) => set({ sent: v })}>
          <SelectTrigger aria-label="sent as" className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            {keys.map((k) => (
              <SelectItem key={k} value={k}>{k === "keep" && installed?.inject ? keepAsWritten(installed.inject.name || "", installed.inject.template || "") : SENT_AS[k].label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        {named && (
          <Input id={named} aria-label={c.sent === "env" ? "variable name" : "header name"} placeholder={c.sent === "env" ? "API_TOKEN" : "X-Api-Key"} spellCheck={false} autoComplete="off"
            className="font-mono" aria-invalid={!!error || undefined} aria-describedby="cs-sent-say"
            value={c.sent === "env" ? c.envName : c.headerName} onChange={(e) => set(c.sent === "env" ? { envName: e.target.value } : { headerName: e.target.value })} />
        )}
        {error ? <p id="cs-sent-say" className={ERROR}>{error}</p> : <p id="cs-sent-say" className={HINT}>{hint}</p>}
      </div>
    );
  };

  const agents = (kind: "token" | "oauth") => (
    <div className="flex flex-col gap-1.5">
      <span className={CAPS}>{"Agents with no " + (kind === "token" ? "token" : "sign-in") + " of their own"}</span>
      <CardGroup label="agents" value={c.agents} onPick={(v) => set({ agents: v })} className="gap-1.5">
        {AGENT_ROWS[kind].map((r) => (
          <OptionCard key={r.value} row value={r.value} label={"agents " + r.value} name={r.name} tag={"agents: " + r.value}
            line={r.value === "shared" ? SHARED_LINE[kind] : r.line} on={c.agents === r.value} />
        ))}
      </CardGroup>
    </div>
  );

  let fields: React.ReactNode = null;
  if (c.kind === "static") {
    fields = (
      <>
        {sentAs()}
        {c.sent === "env" && <p className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-snug text-foreground">{ENV_WARNING_SHORT}</p>}
      </>
    );
  } else if (c.kind === "token") {
    fields = <>{sentAs()}{agents("token")}</>;
  } else if (c.kind === "oauth") {
    const error = sayOf(misses, "cs-provider");
    fields = (
      <>
        <div className="flex flex-col gap-1">
          <span className={CAPS}>Identity provider</span>
          <Select value={c.provider} onValueChange={(v) => { const q = provs.find((x) => x.name === v); set({ provider: v, scopes: ((q && q.scopes) || []).join(" ") }); }}>
            <SelectTrigger id="cs-provider" aria-label="identity provider" className="w-full" aria-invalid={!!error || undefined} aria-describedby="cs-provider-say"><SelectValue placeholder="Pick an identity provider" /></SelectTrigger>
            <SelectContent>{names.map((n) => <SelectItem key={n} value={n}>{n}</SelectItem>)}</SelectContent>
          </Select>
          {error
            ? <p id="cs-provider-say" className={ERROR}>{error}</p>
            : <p id="cs-provider-say" className={HINT}>Register <code className="font-mono text-foreground">{redirect}</code>{" at " + (c.provider || "the provider") + "."}</p>}
        </div>
        <Field id="cs-scopes" label="Scopes" hint="The provider's default, editable. Space-separated.">
          <Input id="cs-scopes" spellCheck={false} autoComplete="off" className="font-mono" aria-describedby="cs-scopes-say" value={c.scopes} onChange={(e) => set({ scopes: e.target.value })} />
        </Field>
        {agents("oauth")}
      </>
    );
  }

  const more = c.kind === "oauth" ? oauthMore(c.provider, redirect) : (CREDENTIAL_MORE[c.kind] || "") + (c.kind === "static" && c.sent === "env" ? " " + ENV_WARNING : "");

  return (
    <>
      <div className="flex flex-col gap-2">
        <span className={CAPS}>What the server sees</span>
        <CardGroup label="credential" value={c.kind} onPick={pick}>
          {cards.map((k) => (
            <OptionCard key={k.kind} value={k.kind} label={"credential " + k.kind} name={k.name} tag={"kind: " + k.kind} line={offLine(k.kind) || k.line}
              on={c.kind === k.kind} off={!!offLine(k.kind)} />
          ))}
        </CardGroup>
        <Fold title="More about this choice">
          <p className="m-0 max-w-[75ch] px-3.5 py-2.5 text-sm leading-relaxed text-text-2">{more}</p>
        </Fold>
      </div>
      {fields
        ? <div className="flex flex-col gap-3.5 rounded-md border border-border bg-card px-4 py-3" data-credential-box><div className="font-semibold text-foreground">{chosen.name}</div>{fields}</div>
        : <p className={HINT}>Nothing to set for this choice.</p>}
    </>
  );
}
