import * as React from "react";
import type { FieldErrors } from "react-hook-form";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { type ApiError, importServerJSON } from "@/lib/api";
import { type Form, type NameCheck, importFields, leaveRemote } from "@/lib/manifest";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { COMMAND_REFUSED, OCI_MISSING, OCI_MISSING_REMOTE, RUNTIMES } from "@/lib/words";
import { cn } from "@/lib/utils";
import { credentialFields, importedDoc, readImport } from "./import-reader";
import { CAPS, CardGroup, ERROR, Field, HINT, ManifestFold, OptionCard, TWO } from "./parts";
import type { Dry } from "./use-dry-run";
import type { FieldFn, SetFn } from "./use-wizard-form";

const NOT_JSON = "This is not JSON. Paste the server.json record from the registry as it is.";
const IMPORT_UNREACHABLE = "strazad is unreachable, so the record could not be converted. Try again once it answers.";

// DryLine is the dry run's verdict in one line under the runtime cards.
export function DryLine({ dry }: { dry: Dry }) {
  if (dry.state === "ok") {
    return (
      <p role="status" className={HINT} data-dry-run="ok">
        <span className="text-ok">{"The server accepts this manifest: " + dry.runtime + " runtime, credential " + dry.credential + "."}</span>
        {" Checked with the server as you type, the same code path as strazactl spec validate."}
      </p>
    );
  }
  const line = dry.state === "refused" ? { cls: ERROR, text: dry.text }
    : dry.state === "unreachable" ? { cls: "text-[13px] leading-snug text-unknown", text: "strazad is unreachable, so the manifest is not checked here. Save and publish will say." }
      : dry.state === "pending" ? { cls: HINT, text: "Checking the manifest with the server." }
        : { cls: HINT, text: "The manifest is checked with the server once it has a name and an address." };
  return <p role="status" className={line.cls} data-dry-run={dry.state}>{line.text}</p>;
}

type Props = {
  f: Form;
  errors: FieldErrors<Form>;
  field: FieldFn;
  set: SetFn;
  check: NameCheck | null;
  runtimes: string[] | null;
  dry: Dry;
  yaml: string;
  importBox: React.RefObject<HTMLTextAreaElement | null>;
};

type Imp = { kind: "busy" } | { kind: "ok"; name: string; kept: string } | { kind: "error"; text: string } | null;

// ServerStep asks the name, the description and how Straza reaches the
// server. The open runtime card carries its fields; the registry card
// converts a pasted server.json on the server and opens the card it read.
export function ServerStep({ f, errors, field, set, check, runtimes, dry, yaml, importBox }: Props) {
  const [json, setJson] = React.useState("");
  const [imp, setImp] = React.useState<Imp>(null);
  const ociOK = !runtimes || runtimes.includes("oci");
  const commandOK = !runtimes || runtimes.includes("command");
  const offLine = (key: string) => (key === "oci" && !ociOK ? (commandOK ? OCI_MISSING : OCI_MISSING_REMOTE) : key === "command" && !commandOK ? COMMAND_REFUSED : "");
  const nameSay = errors.name?.message ? { level: "error" as const, text: errors.name.message, twin: undefined } : check;
  const twin = nameSay && nameSay.twin;

  const pick = (key: string) => {
    if (key === f.mode || offLine(key)) return;
    if (key === "import") { set({ mode: "import" }); return; }
    set({ mode: key, runtime: key, imported: key === f.runtime ? f.imported : null, ...leaveRemote(f, key) });
  };

  const convert = async () => {
    let record: unknown;
    try { record = JSON.parse(json); } catch { setImp({ kind: "error", text: NOT_JSON }); return; }
    setImp({ kind: "busy" });
    try {
      const r = await importServerJSON(record);
      const read = readImport(r.manifest);
      const doc = importedDoc(read, record, r.name, r.runtime);
      const fields = importFields(doc);
      // The conversion's credential lands on its card; one the cards
      // cannot write is left off and named, never rewritten.
      const cred = credentialFields(read.credential);
      const kept = cred === null && read.credential ? read.credential.as + " " + read.credential.name + " with the template " + read.credential.template : "";
      set({ ...fields, ...leaveRemote(f, fields.runtime), ...(cred || { cred: "none" }), mode: fields.runtime, name: f.name.trim() || r.name || "", imported: { doc, text: r.manifest } });
      setImp({ kind: "ok", name: r.name, kept });
    } catch (e) {
      const err = e as ApiError;
      setImp(err.status === 401 ? null : { kind: "error", text: err.unreachable ? IMPORT_UNREACHABLE : err.message });
    }
  };

  const mono = (name: "url" | "exec" | "args" | "image", label: string, placeholder: string, hint?: string) => (
    <Field id={"mw-" + name} label={label} error={errors[name]?.message} hint={hint}>
      <Input id={"mw-" + name} aria-label={label} placeholder={placeholder} spellCheck={false} className="font-mono"
        aria-invalid={!!errors[name] || undefined} aria-describedby={"mw-" + name + "-say"} {...field(name)} />
    </Field>
  );

  const fieldsOf = (key: string) => {
    if (key === "remote") return mono("url", "url", "http://host:port/mcp", "Straza proxies calls to this address and pings it for health.");
    if (key === "command") return <>{mono("exec", "executable", "/usr/local/bin/mcp-server")}{mono("args", "arguments", "--stdio", "Space-separated. The process runs on the strazad host and speaks MCP over stdio.")}</>;
    if (key === "oci") return mono("image", "image", "ghcr.io/org/server:1.2");
    return (
      <>
        <Field id="mw-import" label="server.json" error={errors.mode?.message}>
          <Textarea id="mw-import" ref={importBox} aria-label="server.json" rows={5} spellCheck={false} placeholder={'{"name": "io.github.org/server", ...}'}
            className="field-sizing-fixed font-mono text-[13px]" aria-invalid={!!errors.mode || undefined} aria-describedby="mw-import-say"
            value={json} onChange={(e) => setJson(e.target.value)} />
        </Field>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void convert()} disabled={imp?.kind === "busy"}>
            {imp?.kind === "busy" && <Loader2Icon className="animate-spin" />}
            Convert
          </Button>
          {imp?.kind === "error"
            ? <span className={ERROR}>{imp.text}</span>
            : <span className={HINT}>Straza converts it and fills the cards above; edit them if needed.</span>}
        </div>
      </>
    );
  };

  return (
    <div className="flex flex-col gap-5">
      <div className={TWO}>
        <div className="flex flex-col gap-1">
          <label htmlFor="mw-name" className={CAPS}>name</label>
          <Input id="mw-name" autoFocus aria-label="server name" spellCheck={false} className="font-mono"
            aria-invalid={nameSay?.level === "error" || undefined} aria-describedby="mw-name-say" {...field("name")} />
          {nameSay && (
            <p id="mw-name-say" className={nameSay.level === "error" ? ERROR : nameSay.level === "ok" ? "text-[13px] leading-snug text-ok" : HINT}>
              {nameSay.text}
              {twin && (
                <>
                  {" "}
                  <a href={pathFor("servers", [twin.id])} className="text-link underline underline-offset-4"
                    onClick={(e) => { if (!isPlainClick(e)) return; e.preventDefault(); navigate("servers", [twin.id]); }}>Open it</a>
                </>
              )}
            </p>
          )}
        </div>
        <Field id="mw-desc" label="description" hint="Optional. Shows on the server's page and in the list.">
          <Input id="mw-desc" aria-label="server description" aria-describedby="mw-desc-say" {...field("desc")} />
        </Field>
      </div>
      <div className="flex flex-col gap-2">
        <div className="font-semibold text-foreground">How does Straza reach it?</div>
        <CardGroup label="runtime" value={f.mode} onPick={pick} className="grid-cols-2">
          {RUNTIMES.map((r) => {
            const off = offLine(r.key);
            return (
              <OptionCard key={r.key} value={r.key} label={"runtime " + r.key} name={r.name} tag={r.key} line={off || r.desc}
                on={f.mode === r.key} off={!!off} dashed={r.key === "import"}>
                {f.mode === r.key ? fieldsOf(r.key) : null}
              </OptionCard>
            );
          })}
        </CardGroup>
        {imp?.kind === "ok" && f.imported && (
          <p className={cn(HINT, "text-ok")}>{"Converted: " + imp.name + ", " + f.runtime + " runtime. The cards above show what it said; edit them if needed."}</p>
        )}
        {imp?.kind === "ok" && f.imported && imp.kept && (
          <p className={ERROR}>{"The record's credential (" + imp.kept + ") is not one the credential cards write, so the Credential step starts on None. Install it from the file with strazactl apps install -f to keep that template."}</p>
        )}
      </div>
      <DryLine dry={dry} />
      <ManifestFold yaml={yaml} />
    </div>
  );
}
