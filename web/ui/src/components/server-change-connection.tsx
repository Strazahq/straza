import type * as React from "react";
import { LockIcon, PlusIcon, XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { MISSING, headerMissing } from "@/components/wizard/form-schema";
import { CAPS, CardGroup, ERROR, Field, HINT, OptionCard } from "@/components/wizard/parts";
import type { ManifestDoc } from "@/lib/api";
import { ADDRESS_HINT, ARGS_HINT, ENV_HINT, ENV_LOCKED, ENV_NAMELESS, NAME_LOCKED, TO_ENV, TO_HEADER } from "@/lib/change-words";
import { HTTP_SENT, SENT_AS } from "@/lib/manifest";
import { type Connection, callerKind, injectMoves } from "@/lib/manifest-edit";
import { RUNTIME_NEEDS_GLOBAL } from "@/lib/server-words";
import { adminAreas } from "@/lib/session";
import { CALLER_OFF_SHORT, COMMAND_REFUSED, ENV_WARNING_SHORT, OCI_MISSING, OCI_MISSING_REMOTE, RUNTIMES, sentShort } from "@/lib/words";

// Miss is one field a Change sheet cannot save without: the id of the
// input the primary focuses, and the sentence shown under it.
export type Miss = { id: string; text: string };

export const sayOf = (misses: Miss[], id: string) => misses.find((m) => m.id === id)?.text;

// Locked is a value the sheet shows and never edits, with the reason.
export function Locked({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-card px-3 py-2 text-sm leading-snug text-text-2">
      <LockIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <span>{children}</span>
    </div>
  );
}

// connectionMisses lists what the Connection card cannot save without, in
// the order the card draws it.
export function connectionMisses(base: ManifestDoc, c: Connection): Miss[] {
  const out: Miss[] = [];
  if (c.kind === "remote") {
    const url = c.url.trim();
    if (!url) out.push({ id: "cs-url", text: MISSING.url });
    else if (!/^https?:\/\//i.test(url)) out.push({ id: "cs-url", text: MISSING.scheme });
  }
  if (c.kind === "command") {
    if (!c.exec.trim()) out.push({ id: "cs-exec", text: MISSING.exec });
    const i = c.env.findIndex((e) => !e.name.trim() && e.value);
    if (i >= 0) out.push({ id: "cs-env-" + i, text: ENV_NAMELESS });
  }
  if (c.kind === "oci" && !c.image.trim()) out.push({ id: "cs-image", text: MISSING.image });
  if (injectMoves(base, c.kind)) {
    if (c.kind !== "remote" && !c.envName.trim()) out.push({ id: "cs-cred-env", text: MISSING.envName });
    if (c.kind === "remote" && c.sent === "header" && !c.headerName.trim()) out.push({ id: "cs-cred-header", text: headerMissing("static") });
  }
  return out;
}

type Props = { name: string; base: ManifestDoc; c: Connection; set: (patch: Partial<Connection>) => void; misses: Miss[]; runtimes: string[] | null };

// ConnectionFields is the Connection card's sheet. It shows the name,
// locked, then the three runtime cards with the open card's fields. When
// the transport moves between HTTP and a process, it also asks how the
// shared secret travels now.
export function ConnectionFields({ name, base, c, set, misses, runtimes }: Props) {
  const cred = base.straza?.credential;
  const caller = callerKind(cred?.kind || "none");
  const ociOK = !runtimes || runtimes.includes("oci");
  const commandOK = !runtimes || runtimes.includes("command");
  // A session without the apps grant administers this server through its
  // admin role and may not put a process on the gateway host, so the
  // command and container runtimes are off for it, as strazad refuses them.
  const areas = adminAreas();
  const hostOff = areas !== null && !areas.apps;
  const offLine = (key: string) => (key !== "remote" && hostOff ? RUNTIME_NEEDS_GLOBAL : key !== "remote" && caller ? CALLER_OFF_SHORT : key === "oci" && !ociOK ? (commandOK ? OCI_MISSING : OCI_MISSING_REMOTE) : key === "command" && !commandOK ? COMMAND_REFUSED : "");
  const pick = (key: string) => { if (key !== c.kind && !offLine(key)) set({ kind: key }); };
  const moves = injectMoves(base, c.kind);
  const lockedEnv = !moves && c.kind === "command" && cred?.inject?.as === "env" ? cred.inject.name || "" : "";

  const text = (id: string, label: string, key: "url" | "exec" | "args" | "workdir" | "image" | "envName", placeholder: string, hint?: string) => {
    const error = sayOf(misses, id);
    return (
      <Field id={id} label={label} error={error} hint={hint}>
        <Input id={id} placeholder={placeholder} spellCheck={false} autoComplete="off" className="font-mono" aria-invalid={!!error || undefined}
          aria-describedby={id + "-say"} value={c[key]} onChange={(e) => set({ [key]: e.target.value })} />
      </Field>
    );
  };

  const envRows = () => {
    const error = misses.find((m) => m.id.startsWith("cs-env-"));
    const put = (i: number, patch: Partial<Connection["env"][number]>) => set({ env: c.env.map((e, j) => (j === i ? { ...e, ...patch } : e)) });
    return (
      <div className="flex flex-col gap-1.5">
        <span className={CAPS}>Environment</span>
        {c.env.map((e, i) => (
          <div key={i} className="flex items-center gap-2">
            <Input id={"cs-env-" + i} aria-label={"variable " + (i + 1) + " name"} placeholder="NAME" spellCheck={false} autoComplete="off" className="font-mono"
              aria-invalid={error?.id === "cs-env-" + i || undefined} value={e.name} onChange={(ev) => put(i, { name: ev.target.value })} />
            <Input aria-label={"variable " + (i + 1) + " value"} placeholder="value" spellCheck={false} autoComplete="off" className="font-mono"
              value={e.value} onChange={(ev) => put(i, { value: ev.target.value })} />
            <Button variant="ghost" size="icon-sm" aria-label={"Remove variable " + (e.name || i + 1)} title="Remove this variable"
              onClick={() => set({ env: c.env.filter((_, j) => j !== i) })}><XIcon /></Button>
          </div>
        ))}
        {lockedEnv && <Locked><code className="font-mono text-foreground">{lockedEnv}</code>{ENV_LOCKED}</Locked>}
        <Button variant="ghost" size="sm" className="self-start" onClick={() => set({ env: [...c.env, { name: "", value: "" }] })}><PlusIcon />Add a variable</Button>
        {error ? <p className={ERROR}>{error.text}</p> : <p className={HINT}>{ENV_HINT}</p>}
      </div>
    );
  };

  const secretMove = () => {
    if (!moves) return null;
    if (c.kind !== "remote") {
      return (
        <>
          {text("cs-cred-env", "Shared secret's variable", "envName", "API_TOKEN", TO_ENV)}
          <p className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-snug text-foreground">{ENV_WARNING_SHORT}</p>
        </>
      );
    }
    const error = sayOf(misses, "cs-cred-header");
    return (
      <div className="flex flex-col gap-1">
        <span className={CAPS}>Shared secret sent as</span>
        <p className={HINT}>{TO_HEADER}</p>
        <Select value={c.sent} onValueChange={(v) => set({ sent: v })}>
          <SelectTrigger aria-label="shared secret sent as" className="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>{HTTP_SENT.map((k) => <SelectItem key={k} value={k}>{SENT_AS[k].label}</SelectItem>)}</SelectContent>
        </Select>
        {c.sent === "header" && (
          <Input id="cs-cred-header" aria-label="header name" placeholder="X-Api-Key" spellCheck={false} autoComplete="off" className="font-mono"
            aria-invalid={!!error || undefined} aria-describedby="cs-cred-header-say" value={c.headerName} onChange={(e) => set({ headerName: e.target.value })} />
        )}
        {error ? <p id="cs-cred-header-say" className={ERROR}>{error}</p> : <p id="cs-cred-header-say" className={HINT}>{sentShort(c.sent, "static", c.headerName)}</p>}
      </div>
    );
  };

  const fieldsOf = (key: string) => {
    if (key === "remote") return <>{text("cs-url", "Address", "url", "http://host:port/mcp", ADDRESS_HINT)}{secretMove()}</>;
    if (key === "oci") return <>{text("cs-image", "Image", "image", "ghcr.io/org/server:1.2")}{secretMove()}</>;
    return (
      <>
        {text("cs-exec", "Executable", "exec", "/usr/local/bin/mcp-server")}
        {text("cs-args", "Arguments", "args", "--stdio", ARGS_HINT)}
        {text("cs-workdir", "Working directory", "workdir", "strazad's own when empty")}
        {envRows()}
        {secretMove()}
      </>
    );
  };

  return (
    <>
      <div className="flex flex-col gap-1">
        <span className={CAPS}>Name</span>
        <Locked><code className="font-mono text-foreground">{name}</code><br />{NAME_LOCKED}</Locked>
      </div>
      <div className="flex flex-col gap-2">
        <span className={CAPS}>How Straza reaches it</span>
        <CardGroup label="runtime" value={c.kind} onPick={pick}>
          {RUNTIMES.filter((r) => r.key !== "import").map((r) => (
            <OptionCard key={r.key} value={r.key} label={"runtime " + r.key} name={r.name} tag={r.key} line={offLine(r.key) || r.desc}
              on={c.kind === r.key} off={!!offLine(r.key)}>
              {c.kind === r.key ? fieldsOf(r.key) : null}
            </OptionCard>
          ))}
        </CardGroup>
      </div>
    </>
  );
}
