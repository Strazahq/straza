import * as React from "react";
import type { FieldErrors } from "react-hook-form";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ManifestFold } from "@/components/yaml-fold";
import { type ApiError, importServerJSON } from "@/lib/api";
import { type Form, type NameCheck, importFields, leaveRemote, nameFromAddress } from "@/lib/manifest";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { ACCEPTS, MANIFEST_PENDING, MANIFEST_UNREACHABLE, converted } from "@/lib/save-words";
import { ADDRESS_HINT, COMMAND_OFF, DESC_HINT, OCI_OFF, OTHER_WAYS, RUNTIMES, SERVER_NAME_HINT } from "@/lib/words";
import { cn } from "@/lib/utils";
import { credentialFields, importedDoc, readImport } from "./import-reader";
import { CAPS, CardGroup, ERROR, Field, Fold, HINT, OptionCard } from "./parts";
import type { Dry } from "./use-dry-run";
import type { FieldFn, SetFn } from "./use-wizard-form";

const NOT_JSON = "This is not JSON. Paste the server.json record from the registry as it is.";
const IMPORT_UNREACHABLE = "Straza is unreachable, so the record could not be converted. Try again once it answers.";
const IMPORT_HINT = "Convert fills in the fields.";
// WIDE is the width of the address, the other ways' fields and the
// description. The name box is shorter.
const WIDE = "w-full max-w-[560px]";

// DryLine is the dry run's verdict in one line under the step's fields,
// and nothing before the dry run has run.
export function DryLine({ dry }: { dry: Dry }) {
  if (dry.state === "ok") {
    return (
      <p role="status" className={HINT} data-dry-run="ok">
        <span className="text-ok">{ACCEPTS}</span>
      </p>
    );
  }
  const line = dry.state === "refused" ? { cls: ERROR, text: dry.text }
    : dry.state === "unreachable" ? { cls: "text-caption leading-snug text-unknown", text: MANIFEST_UNREACHABLE }
      : dry.state === "pending" ? { cls: HINT, text: MANIFEST_PENDING }
        : null;
  if (!line) return null;
  return <p role="status" className={cn(line.cls, "[overflow-wrap:anywhere]")} data-dry-run={dry.state}>{line.text}</p>;
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
  // named says the person typed in the name box, after which the name no
  // longer follows the address. The wizard holds it, so a step back keeps it.
  named: React.RefObject<boolean>;
};

type Imp = { kind: "busy" } | { kind: "ok"; name: string; kept: string } | { kind: "error"; text: string } | null;

// ServerStep asks where the server is, then its name, then its
// description. The ways this server can use are radio rows when there is
// more than one, and the others sit under a closed fold that says why. The
// open import box stands in for the ways until its record is converted.
export function ServerStep({ f, errors, field, set, check, runtimes, dry, yaml, importBox, named }: Props) {
  const [json, setJson] = React.useState("");
  const [imp, setImp] = React.useState<Imp>(null);
  const ociOK = !runtimes || runtimes.includes("oci");
  const commandOK = !runtimes || runtimes.includes("command");
  const offLine = (key: string) => (key === "oci" && !ociOK ? OCI_OFF : key === "command" && !commandOK ? COMMAND_OFF : "");
  // A way this server cannot use stays a row while it is the picked one, as
  // after an import, so its answers stay in view.
  const shown = RUNTIMES.filter((r) => !offLine(r.key) || r.key === f.mode);
  const others = RUNTIMES.filter((r) => !shown.includes(r));
  const nameSay = errors.name?.message ? { level: "error" as const, text: errors.name.message, twin: undefined } : check;
  const twin = nameSay && nameSay.twin;
  const url = field("url");
  const name = field("name");

  // The address is the first answer, so the step opens on it.
  React.useEffect(() => { document.getElementById("mw-url")?.focus(); }, []);

  const pick = (key: string) => {
    if (key === f.mode || offLine(key)) return;
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
      set({ ...fields, ...leaveRemote(f, fields.runtime), ...(cred || { cred: "none" }), mode: fields.runtime, name: (named.current && f.name.trim()) || r.name || "", imported: { doc, text: r.manifest } });
      setImp({ kind: "ok", name: r.name, kept });
    } catch (e) {
      const err = e as ApiError;
      setImp(err.status === 401 ? null : { kind: "error", text: err.unreachable ? IMPORT_UNREACHABLE : err.message });
    }
  };

  const mono = (key: "exec" | "args" | "image", label: string, placeholder: string, hint?: string) => (
    <Field id={"mw-" + key} label={label} error={errors[key]?.message} hint={hint}>
      <Input id={"mw-" + key} aria-label={label} placeholder={placeholder} spellCheck={false} className="font-mono"
        aria-invalid={!!errors[key] || undefined} aria-describedby={"mw-" + key + "-say"} {...field(key)} />
    </Field>
  );

  const fieldsOf = (key: string) => {
    if (key === "command") return <>{mono("exec", "executable", "/usr/local/bin/mcp-server")}{mono("args", "arguments", "--stdio", "Space-separated.")}</>;
    if (key === "oci") return mono("image", "image", "ghcr.io/org/server:1.2");
    return (
      <Field id="mw-url" label="address" error={errors.url?.message} hint={ADDRESS_HINT}>
        <Input id="mw-url" aria-label="server address" placeholder="http://host:port/mcp" spellCheck={false} className="font-mono"
          aria-invalid={!!errors.url || undefined} aria-describedby="mw-url-say" {...url}
          onChange={(e) => { void url.onChange(e); if (!named.current) set({ name: nameFromAddress(e.target.value) }); }} />
      </Field>
    );
  };

  const reach = f.mode === "import" ? (
    <div className={cn("flex flex-col gap-2.5 rounded-md border border-dashed border-border p-3", WIDE)} data-import>
      <Field id="mw-import" label="server.json" error={errors.mode?.message}>
        <Textarea id="mw-import" ref={importBox} autoFocus aria-label="server.json" rows={5} spellCheck={false} placeholder={'{"name": "io.github.org/server", ...}'}
          className="field-sizing-fixed font-mono text-caption" aria-invalid={!!errors.mode || undefined} aria-describedby="mw-import-say"
          value={json} onChange={(e) => setJson(e.target.value)} />
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onClick={() => void convert()} disabled={imp?.kind === "busy"}>
          {imp?.kind === "busy" && <Loader2Icon className="animate-spin" />}
          Convert
        </Button>
        {imp?.kind === "error" ? <span className={ERROR}>{imp.text}</span> : <span className={HINT}>{IMPORT_HINT}</span>}
      </div>
    </div>
  ) : (
    <div className={cn("flex flex-col gap-2.5", WIDE)}>
      {shown.length > 1 && (
        <CardGroup label="runtime" value={f.mode} onPick={pick} className="gap-1.5">
          {shown.map((r) => (
            <OptionCard key={r.key} value={r.key} name={r.name} line={offLine(r.key) || r.line} on={f.mode === r.key} off={!!offLine(r.key)} row />
          ))}
        </CardGroup>
      )}
      {fieldsOf(f.mode)}
      {imp?.kind === "ok" && f.imported && (
        <p className={cn(HINT, "text-ok")}>{converted(imp.name)}</p>
      )}
      {imp?.kind === "ok" && f.imported && imp.kept && (
        <p className={ERROR}>{"The record's credential (" + imp.kept + ") is not one the credential cards write, so the Credential step starts on None. Install it from the file with strazactl apps install -f to keep that template."}</p>
      )}
    </div>
  );

  return (
    <div className="flex flex-col gap-5">
      {reach}
      <div className="flex flex-col gap-1">
        <label htmlFor="mw-name" className={CAPS}>name</label>
        <Input id="mw-name" aria-label="server name" spellCheck={false} className="w-[280px] max-w-full font-mono"
          aria-invalid={nameSay?.level === "error" || undefined} aria-describedby="mw-name-say mw-name-hint" {...name}
          onChange={(e) => { named.current = true; void name.onChange(e); }} />
        {nameSay && (
          <p id="mw-name-say" className={nameSay.level === "error" ? ERROR : nameSay.level === "ok" ? "text-caption leading-snug text-ok" : HINT}>
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
        <p id="mw-name-hint" className={HINT}>{SERVER_NAME_HINT}</p>
      </div>
      <Field id="mw-desc" label="description" hint={DESC_HINT}>
        <Input id="mw-desc" aria-label="server description" aria-describedby="mw-desc-say" className={WIDE} {...field("desc")} />
      </Field>
      <DryLine dry={dry} />
      {others.length > 0 && (
        <Fold title={OTHER_WAYS}>
          <ul className="m-0 flex list-none flex-col gap-2 px-3 py-2.5" data-other-ways>
            {others.map((r) => (
              <li key={r.key} className="flex flex-col gap-0.5" data-way-off={r.key}>
                <span className="text-dense font-semibold text-foreground">{r.name}</span>
                <span className={HINT}>{offLine(r.key)}</span>
              </li>
            ))}
          </ul>
        </Fold>
      )}
      <ManifestFold yaml={yaml} />
    </div>
  );
}
