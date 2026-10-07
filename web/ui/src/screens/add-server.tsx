import * as React from "react";
import type { FieldPath } from "react-hook-form";
import { DownloadIcon, Loader2Icon, LockIcon, RefreshCwIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { PageHead } from "@/components/page-head";
import { TestACall } from "@/components/test-a-call";
import { SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { CheckStep } from "@/components/wizard/check-step";
import { CredentialStep } from "@/components/wizard/credential-step";
import { STEP_KEYS, stepFields } from "@/components/wizard/form-schema";
import { HINT, type Row } from "@/components/wizard/parts";
import { ReviewStep } from "@/components/wizard/review-step";
import { ServerStep } from "@/components/wizard/server-step";
import { StepStrip } from "@/components/wizard/step-strip";
import { useDryRun } from "@/components/wizard/use-dry-run";
import { useInstall } from "@/components/wizard/use-install";
import { useWizardForm } from "@/components/wizard/use-wizard-form";
import { type AppRow, type ProviderRow, listApps, listProviders } from "@/lib/api";
import { EMPTY_FORM, type Form, NAME_RE, SERVER_PICK_ANOTHER, appNameCheck, buildManifest, manifestReady, manifestYAML, needsSecret, serverTaken } from "@/lib/manifest";
import { version } from "@/lib/public";
import { SAVE_DRAFT, SAVE_PUBLISH, SECRET_WAITS, publishRow } from "@/lib/save-words";
import { registerNeedsGlobal } from "@/lib/server-words";
import { adminAreas, adminServers } from "@/lib/session";
import { navigate } from "@/lib/router";
import { LEDE } from "@/lib/words";
import { cn, downloadText } from "@/lib/utils";

const LABELS = ["Server", "Credential", "Review", "Check"];
const soft = <T,>(p: Promise<T>) => p.catch(() => null);

// AddServer is the Add MCP server wizard at /console/servers/new: Server,
// Credential, Review, Check. Review ends in Save draft, which adds the
// manifest to the person's working draft, and Save and publish, which
// publishes it and then stores the secret and probes.
// Nothing is live before the publish; after it the server exists, so Back
// from Credential is gone, Cancel means leave, and a return to Credential
// stores and probes without a second publish.
export function AddServer() {
  const { form, f, field, set } = useWizardForm();
  const errors = form.formState.errors;
  const [at, setAt] = React.useState(0);
  const [apps, setApps] = React.useState<AppRow[] | null | undefined>(undefined);
  const [runtimes, setRuntimes] = React.useState<string[] | null>(null);
  const [providers, setProviders] = React.useState<ProviderRow[] | null | undefined>(undefined);
  const [ask, setAsk] = React.useState(false);
  const [testOpen, setTestOpen] = React.useState(false);
  const importBox = React.useRef<HTMLTextAreaElement>(null);
  const heading = React.useRef<HTMLHeadingElement>(null);
  const shown = React.useRef(at);
  const install = useInstall();

  // Registering needs the apps grant: a session that administers servers
  // through their admin roles reads why and where to go, and asks for none
  // of the lists the wizard needs.
  const areas = adminAreas();
  const locked = areas !== null && !areas.apps;
  const loadApps = React.useCallback(() => soft(listApps()).then((r) => setApps(Array.isArray(r) ? r : null)), []);
  React.useEffect(() => {
    let alive = true;
    if (locked) return () => { alive = false; };
    void loadApps();
    soft(version()).then((v) => { if (alive) setRuntimes(v && Array.isArray(v.runtimes) ? v.runtimes : null); });
    soft(listProviders()).then((p) => { if (alive) setProviders(Array.isArray(p) ? p : null); });
    return () => { alive = false; };
  }, [loadApps, locked]);
  // A new step moves focus to its heading, so a keyboard user starts there
  // and not on a button that left with the old step.
  React.useEffect(() => {
    if (shown.current === at) return;
    shown.current = at;
    heading.current?.focus();
  }, [at]);

  const step = STEP_KEYS[at];
  const name = f.name.trim();
  const yaml = manifestYAML(buildManifest(f));
  const check = apps === undefined && NAME_RE.test(name) ? null : appNameCheck(f.name, apps ?? null);
  const landed = install.landed;
  const installed = landed ? landed.app : null;
  const ready = manifestReady(f) && !(check && check.level === "error");
  const dry = useDryRun(yaml, installed || step === "check" ? "off" : step === "review" ? "once" : ready ? "live" : "off");
  const refused = dry.state === "refused";
  const secretFor = () => (needsSecret(f) ? form.getValues("secret") : null);
  // A name that went live after the list was read, or that no list could
  // check, is refused by the check's existed answer, never saved over.
  const saver = useDraftSave({
    name: name || "the server",
    creates: { object: "App/" + name, taken: serverTaken(name) + " " + SERVER_PICK_ANOTHER },
    onPublished: () => {
      void install.afterPublish({ name, url: f.runtime === "remote" ? f.url.trim() : "", secret: secretFor(), onSecretTried: () => form.setValue("secret", "") }).then((reached) => { if (reached) setAt(3); });
    },
  });
  const items = [{ kind: "App" as const, name, op: "put" as const, doc: yaml }];

  const focusFirst = (names: FieldPath<Form>[]) => {
    const bad = names.find((n) => form.getFieldState(n).error);
    if (bad === "mode") importBox.current?.focus();
    else if (bad) form.setFocus(bad);
  };

  // next checks the step's answers: the button stays
  // clickable, and a click with something missing focuses it and says so.
  const next = async () => {
    const names = stepFields(step, f);
    if (!(await form.trigger(names))) { focusFirst(names); return; }
    if (step === "server" && check && check.level === "error") { form.setFocus("name"); return; }
    setAt(at + 1);
  };
  const back = () => {
    if (!installed) install.reset();
    setAt(at - 1);
  };

  // send runs Review's two saves: Save and publish, which the secret and the
  // probe follow once the publish landed, and Save draft, which keeps the
  // manifest alone.
  const send = (publish: boolean) => {
    if (install.busy || saver.busy || refused) return;
    void (publish ? saver.saveAndPublish(items) : saver.saveDraft(items));
  };

  // storeAgain is Credential's door once the server exists: store the
  // secret again and probe, with no second publish.
  const storeAgain = async () => {
    if (install.busy || !installed) return;
    const names = stepFields(step, f);
    if (!(await form.trigger(names))) { focusFirst(names); return; }
    const reached = await install.afterPublish({ name: installed.name, url: f.runtime === "remote" ? f.url.trim() : "", secret: secretFor(), onSecretTried: () => form.setValue("secret", "") });
    if (reached) setAt(3);
  };

  const another = () => {
    form.reset(EMPTY_FORM);
    install.reset();
    setTestOpen(false);
    setAt(0);
    void loadApps();
  };

  const dirty = !!(name || f.desc.trim() || f.url.trim() || f.exec.trim() || f.image.trim());
  const cancel = () => { if (installed || dirty) setAsk(true); else navigate("servers"); };

  const label = name || "the server";
  const planned: Row[] = [
    { key: "install", label: publishRow(label), state: "pending" },
    ...(needsSecret(f) ? [{ key: "secret", label: "Store its secret", state: "pending" } as Row] : []),
    { key: "check", label: "Check " + label, state: "pending" },
  ];
  const spin = <Loader2Icon className="animate-spin" />;
  const push = <span className="flex-1" />;

  let body: React.ReactNode = null;
  let footer: React.ReactNode = null;
  if (step === "server") {
    body = <ServerStep f={f} errors={errors} field={field} set={set} check={check} runtimes={runtimes} dry={dry} yaml={yaml} importBox={importBox} />;
    footer = <>{push}<Button onClick={() => void next()}>Next</Button></>;
  } else if (step === "credential") {
    body = <CredentialStep f={f} errors={errors} control={form.control} field={field} set={set} providers={providers} installed={installed ? installed.name : ""} dry={dry} yaml={yaml} />;
    footer = installed
      ? <>{push}<Button onClick={() => void storeAgain()} disabled={install.busy}>{install.busy && spin}Store the secret and check</Button></>
      : <><Button variant="ghost" onClick={back}>Back</Button>{push}<Button onClick={() => void next()}>Next</Button></>;
  } else if (step === "review") {
    body = (
      <>
        <ReviewStep f={f} yaml={yaml} dry={dry} rows={landed ? landed.rows : planned} ran={!!landed} problem={install.problem} />
        {saver.note && <SaveNoteLine note={saver.note} />}
        {needsSecret(f) && <p className={cn(HINT, "max-w-[75ch]")} data-secret-waits>{SECRET_WAITS}</p>}
        {saver.dialog}
      </>
    );
    const busy = install.busy || saver.busy !== null;
    const off = { "aria-disabled": refused || undefined, title: refused ? dry.text : undefined, className: "aria-disabled:cursor-not-allowed aria-disabled:opacity-50" };
    footer = (
      <>
        <Button variant="ghost" onClick={back} disabled={busy}>Back</Button>
        {push}
        <Button variant="outline" {...off} onClick={() => send(false)} disabled={busy}>{saver.busy === "draft" && spin}{SAVE_DRAFT}</Button>
        <Button {...off} onClick={() => send(true)} disabled={busy}>{(saver.busy === "publish" || install.busy) && spin}{SAVE_PUBLISH}</Button>
      </>
    );
  } else if (landed && installed) {
    const running = landed.read ? landed.read.kind === "running" : false;
    const open = () => navigate("servers", [installed.id]);
    body = <CheckStep rows={landed.rows} read={landed.read} name={installed.name} tools={installed.tools || []} problem={install.problem} />;
    footer = running ? (
      <>
        <Button variant="outline" onClick={() => downloadText(installed.name + ".yaml", yaml, "application/yaml")}><DownloadIcon />Download app.yaml</Button>
        {push}
        <Button variant="ghost" onClick={another}>Add another server</Button>
        <Button variant="outline" onClick={() => setTestOpen(true)}>Test a call</Button>
        <Button onClick={open}>{"Open " + installed.name}</Button>
      </>
    ) : (
      <>
        {landed.read && landed.read.kind === "refused" && needsSecret(f) && <Button variant="outline" onClick={() => setAt(1)} disabled={install.busy}>Set the secret again</Button>}
        <Button variant="outline" onClick={() => void install.recheck()} disabled={install.busy}><RefreshCwIcon className={cn(install.busy && "animate-spin")} />Recheck</Button>
        {push}
        <Button variant="ghost" onClick={open}>{"Finish later, open " + installed.name}</Button>
      </>
    );
  }

  if (locked) {
    return (
      <>
        <PageHead label="MCP servers" title="Add MCP server" description="Registering a server is a global admin's door." />
        <EmptyState icon={LockIcon} title="Add MCP server is not available to this account." action={<Button variant="outline" onClick={() => navigate("servers")}>Open MCP servers</Button>}>
          {registerNeedsGlobal(adminServers())}
        </EmptyState>
      </>
    );
  }

  return (
    <div className="flex min-h-full flex-col">
      <PageHead
        label="MCP servers"
        title="Add MCP server"
        description="One door from the manifest to a server Straza fronts. Four steps, and nothing goes live until you publish."
        actions={step !== "check" ? <Button variant="ghost" onClick={cancel} disabled={install.busy}>Cancel</Button> : undefined}
      />
      <div className="flex flex-1 flex-col gap-5 px-6 pt-5 pb-6">
        <StepStrip labels={LABELS} at={at} />
        <div>
          <h2 ref={heading} tabIndex={-1} className="text-base font-semibold text-foreground outline-none">{LABELS[at]}</h2>
          <p className="mt-0.5 max-w-[80ch] text-sm text-muted-foreground">{LEDE[step]}</p>
        </div>
        {body}
      </div>
      <div className="sticky bottom-0 flex items-center gap-2 border-t border-border bg-background px-6 py-3" data-wizard-foot>{footer}</div>
      <AlertDialog open={ask} onOpenChange={setAsk}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{installed ? "Leave the wizard?" : "Discard these answers?"}</AlertDialogTitle>
            <AlertDialogDescription>
              {installed
                ? <>Leave the wizard. <b className="font-semibold text-foreground">{installed.name}</b>{" stays installed and reads " + installed.status + " on the MCP servers page. You can finish access later from its page."}</>
                : "Nothing has been created yet. Discarding loses " + (name ? "the name " + name + " and " : "") + "the answers so far."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{installed ? "Keep going" : "Keep editing"}</AlertDialogCancel>
            <AlertDialogAction className={installed ? undefined : "bg-danger text-white hover:bg-danger/90"} onClick={() => navigate("servers")}>
              {installed ? "Leave" : "Discard the answers"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {installed && <TestACall open={testOpen} onOpenChange={setTestOpen} app={{ id: installed.id, name: installed.name }} tools={installed.tools || []} />}
    </div>
  );
}
