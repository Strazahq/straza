import * as React from "react";
import { Chip } from "@/components/chip";
import { KindTile } from "@/components/kind-icon";
import { ManifestFold } from "@/components/yaml-fold";
import { type ManifestDoc, getConfig } from "@/lib/api";
import { type Form, buildManifest, injectFor } from "@/lib/manifest";
import {
  ACCEPTED, CREDENTIAL_FACT, MANIFEST_PENDING, MANIFEST_UNREACHABLE, NOBODY_UNTIL_ROLE, REFUSED, RUNS, SERVER_CARD, SERVER_FACT,
  TOOLS_ALL, WHAT_WILL_HAPPEN, limitsDefault,
} from "@/lib/save-words";
import { exposeWords, rpsWords, timeoutWords } from "@/lib/server-words";
import { AGENT_ROWS } from "@/lib/words";
import { cn } from "@/lib/utils";
import { CAPS, HINT, Landed, type Problem, ProblemBlock, ReviewFacts, type Row } from "./parts";
import type { Dry } from "./use-dry-run";

type Props = { f: Form; yaml: string; dry: Dry; rows: Row[]; ran: boolean; problem: Problem | null };

const MONO = "font-mono text-caption [overflow-wrap:anywhere]";
const BANNER = "flex flex-wrap items-center gap-2 rounded-md border px-3 py-2.5 text-dense text-foreground";

// secretFate says what happens to the secret, one sentence per credential.
function secretFate(f: Form): string {
  if (f.cred === "static") return "Stored encrypted after publishing. Never shown again.";
  if (f.cred === "token") {
    return f.agents === "shared"
      ? "The shared secret for agents is stored encrypted after publishing. Each person pastes their own token on the Credentials tab of their self-service page."
      : "Nothing is stored now. Each person pastes their own token on the Credentials tab of their self-service page after publishing.";
  }
  if (f.cred === "oauth") return "Nothing is stored now. Each person signs in through " + (f.provider || "the provider") + " on the Credentials tab of their self-service page.";
  return "No secret is stored; the server takes none.";
}

// Verdict is the dry run's answer on the manifest as it will be sent, as
// one banner in its tone.
function Verdict({ dry }: { dry: Dry }) {
  if (dry.state === "ok") {
    return (
      <div className={cn(BANNER, "border-ok/40 bg-ok-bg")} data-verdict="ok">
        <Chip tone="ok">{ACCEPTED}</Chip>
      </div>
    );
  }
  if (dry.state === "refused") {
    return (
      <div className={cn(BANNER, "border-danger/40 bg-danger-bg")} data-verdict="refused">
        <Chip tone="danger">{REFUSED}</Chip>
        <span className="min-w-0 [overflow-wrap:anywhere]">{dry.text}</span>
      </div>
    );
  }
  if (dry.state === "unreachable") return <p className={cn(BANNER, "m-0 border-unknown/40 bg-unknown-bg text-unknown")} data-verdict="unreachable">{MANIFEST_UNREACHABLE}</p>;
  return <p className={cn(HINT, "m-0")} data-verdict="pending">{MANIFEST_PENDING}</p>;
}

// runsFact says where the server runs, then the address, the command with
// its arguments, or the image, as the manifest writes it.
function runsFact(f: Form, doc: ManifestDoc): React.ReactNode {
  const rt = doc.straza?.runtime || {};
  const lead = (RUNS as Record<string, string>)[f.runtime] || "";
  const where = f.runtime === "remote" ? rt.remote?.url : f.runtime === "command" ? [rt.command?.exec || "", ...(rt.command?.args || [])].join(" ") : rt.oci?.image;
  return <>{lead} <span className={MONO}>{where || ""}</span></>;
}

// credentialFact says whose credential the server sees and where it
// travels, with what happens to the secret under it.
function credentialFact(f: Form): React.ReactNode {
  const inject = injectFor(f);
  const mono = (s: string) => <span className={MONO}>{s}</span>;
  let lead: React.ReactNode = CREDENTIAL_FACT.none;
  if (f.cred === "static") lead = inject.as === "env" ? <>{CREDENTIAL_FACT.staticEnv} {mono(inject.name)}</> : <>{CREDENTIAL_FACT.staticHeader} {mono(inject.name)} {CREDENTIAL_FACT.header}</>;
  if (f.cred === "token") lead = <>{CREDENTIAL_FACT.token} {mono(inject.name)} {CREDENTIAL_FACT.header}</>;
  if (f.cred === "oauth") lead = <>{CREDENTIAL_FACT.oauth} {mono(f.provider)}</>;
  return (
    <>
      {lead}
      {/* A server that takes no credential has no secret to say more about. */}
      {f.cred !== "none" && <span className="mt-0.5 block text-caption text-muted-foreground" data-secret-fate>{secretFate(f)}</span>}
    </>
  );
}

// ReviewStep shows what will be created and what will happen before the
// one commit: the server's facts in words on the left, the server's verdict
// and the numbered rows of Save and publish on the right, and the manifest
// under a closed fold. rows are numbered until they run, then show the
// state each landed in.
export function ReviewStep({ f, yaml, dry, rows, ran, problem }: Props) {
  // seconds is the server-wide time per call and remoteRps the rate limit
  // a remote server without its own gets, both read once as the step opens.
  // A session that may not read the config, or a read that fails, leaves
  // them unnamed.
  const [seconds, setSeconds] = React.useState<number | null>(null);
  const [remoteRps, setRemoteRps] = React.useState<number | null>(null);
  React.useEffect(() => {
    let alive = true;
    getConfig().then(
      (c) => { if (alive) { setSeconds(c?.apps?.upstream_timeout_seconds || null); setRemoteRps(c?.apps?.default_remote_rps || null); } },
      () => { if (alive) { setSeconds(null); setRemoteRps(null); } },
    );
    return () => { alive = false; };
  }, []);

  const doc = buildManifest(f);
  const exposed = doc.straza?.exposure?.tools || [];
  const limits = doc.straza?.limits;
  const rate = rpsWords(limits?.rps, (doc.straza?.runtime?.kind || "remote") === "remote", remoteRps);
  const agents = f.cred === "token" || f.cred === "oauth" ? (AGENT_ROWS[f.cred] || []).find((r) => r.value === f.agents) : undefined;
  const facts: [string, React.ReactNode][] = [
    [SERVER_FACT.name, <span key="name" className={MONO}>{f.name.trim()}</span>],
    ...(f.desc.trim() ? [[SERVER_FACT.description, f.desc.trim()] as [string, React.ReactNode]] : []),
    [SERVER_FACT.runs, runsFact(f, doc)],
    [SERVER_FACT.credential, credentialFact(f)],
    ...(agents ? [[SERVER_FACT.agents, agents.name] as [string, React.ReactNode]] : []),
    [SERVER_FACT.tools, exposed.some((t) => t !== "*") ? exposeWords(exposed) : TOOLS_ALL],
    [SERVER_FACT.limits, limits && (limits.rps || limits.timeoutSeconds) ? timeoutWords(limits.timeoutSeconds, seconds) + " " + rate : limitsDefault(seconds, rate)],
  ];

  return (
    <div className="flex flex-col gap-5">
      <div className="grid items-start gap-5 lg:grid-cols-2">
        <section className="min-w-0 rounded-md border border-border bg-card" data-server-card>
          <div className="flex items-center gap-2 border-b border-border px-4 py-2.5">
            <KindTile kind="server" />
            <h3 className="text-body font-semibold text-foreground">{SERVER_CARD}</h3>
          </div>
          <div className="px-4 py-3">
            <ReviewFacts rows={facts} />
          </div>
        </section>
        <section className="flex min-w-0 flex-col gap-4">
          <Verdict dry={dry} />
          <div className="flex flex-col gap-1.5">
            <h3 className={CAPS}>{WHAT_WILL_HAPPEN}</h3>
            <Landed rows={rows} numbered={!ran} />
            <p className="m-0 text-dense text-muted-foreground" data-nobody-until-role>{NOBODY_UNTIL_ROLE}</p>
          </div>
          <ProblemBlock problem={problem} />
        </section>
      </div>
      <ManifestFold yaml={yaml} />
    </div>
  );
}
