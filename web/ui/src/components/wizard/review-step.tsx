import { Badge } from "@/components/ui/badge";
import type { Form } from "@/lib/manifest";
import { cn } from "@/lib/utils";
import { CAPS, Code, HINT, Landed, type Problem, ProblemBlock, type Row } from "./parts";
import type { Dry } from "./use-dry-run";

type Props = { f: Form; yaml: string; dry: Dry; rows: Row[]; ran: boolean; problem: Problem | null };

const BADGE = "rounded-md px-3 py-1 font-mono text-base font-normal";
// STRIP sets the verdict at section size, the headline of the step.
const STRIP = "rounded-md border p-4 text-lg font-semibold";

// secretFate says what happens to the secret, one sentence per credential.
function secretFate(f: Form, name: string): string {
  if (f.cred === "static") return "The secret you typed is stored sealed once " + name + " is installed, and the check runs under it. It never leaves this browser in a URL and is never shown again.";
  if (f.cred === "token") {
    return f.agents === "shared"
      ? "The shared secret for agents is stored sealed after the install. Each person pastes their own token on the Credentials tab of their self-service page."
      : "Nothing is stored now. Each person pastes their own token on the Credentials tab of their self-service page after install.";
  }
  if (f.cred === "oauth") return "Nothing is stored now. Each person signs in through " + (f.provider || "the provider") + " from their Connections page.";
  return "No secret is stored; the server takes none.";
}

// Verdict is the dry run's answer on the manifest as it will be sent.
function Verdict({ f, dry }: { f: Form; dry: Dry }) {
  if (dry.state === "ok" || dry.state === "refused") {
    const ok = dry.state === "ok";
    return (
      <div className={cn(STRIP, "flex items-center gap-4 text-foreground", ok ? "border-ok/40 bg-ok-bg" : "border-danger/40 bg-danger-bg")} data-verdict={dry.state}>
        <Badge variant="outline" className={cn(BADGE, ok ? "border-ok/40 bg-ok-bg text-ok" : "border-danger/40 bg-danger-bg text-danger")}>{ok ? "accepted" : "refused"}</Badge>
        <span>{ok ? "The server accepts this manifest: " + (dry.runtime || f.runtime) + " runtime, credential " + (dry.credential || f.cred) + ". Checked with the dry run a moment ago." : dry.text}</span>
      </div>
    );
  }
  if (dry.state === "unreachable") {
    return <p className={cn(STRIP, "border-unknown/40 bg-unknown-bg text-unknown")} data-verdict="unreachable">strazad is unreachable, so the manifest is not checked here. Save and publish will say.</p>;
  }
  return <p className={HINT} data-verdict="pending">Checking the manifest with the server.</p>;
}

// ReviewStep shows what will be sent and what will happen before the one
// commit. rows are the three steps of Save and publish: numbered until
// they run, then the state each landed in.
export function ReviewStep({ f, yaml, dry, rows, ran, problem }: Props) {
  const name = f.name.trim() || "the server";
  return (
    <div className="grid items-start gap-5 grid-cols-[repeat(auto-fit,minmax(320px,1fr))]">
      <section className="flex min-w-0 flex-col gap-1.5">
        <div className={CAPS}>the manifest that will be sent</div>
        <Code>{yaml}</Code>
      </section>
      <section className="flex min-w-0 flex-col gap-3.5">
        <div className="flex flex-col gap-1.5">
          <div className={CAPS}>the server's verdict</div>
          <Verdict f={f} dry={dry} />
        </div>
        <div className="flex flex-col gap-1.5">
          <div className={CAPS}>the secret</div>
          <p className="text-sm text-text-2">{secretFate(f, name)}</p>
        </div>
        <div className="flex flex-col gap-1.5">
          <div className={CAPS}>what will happen</div>
          <Landed rows={ran ? rows : rows.map((r) => (r.key === "check" ? { ...r, label: r.label + ": Straza starts it or pings it and reads its tool list" } : r))} numbered={!ran} />
        </div>
        <ProblemBlock problem={problem} />
        <p className={HINT}>Exposure stays every tool and limits stay the defaults: the wizard writes only what it asked. Advanced manifests stay files.</p>
      </section>
    </div>
  );
}
