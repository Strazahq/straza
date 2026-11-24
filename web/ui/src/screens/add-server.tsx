import * as React from "react";
import type { FieldPath } from "react-hook-form";
import { DownloadIcon, Loader2Icon, LockIcon, RefreshCwIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button, DANGER_FILL } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { PageHead } from "@/components/page-head";
import { TestACall } from "@/components/test-a-call";
import { SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { CheckStep, whatNextShown } from "@/components/wizard/check-step";
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
import {
  ADD_SERVER_TITLE, BACK, CRUMB_ADD, RECHECK, SAVE_DRAFT, SAVE_PUBLISH, SECRET_WAITS, SERVER_STEPS, SET_AGAIN, STORE_AND_CHECK,
  checkRow, finishLater, liveNow, nextTo, publishRow,
} from "@/lib/save-words";
import { openServer } from "@/lib/server-roles-words";
import { REGISTER_NEEDS_GLOBAL } from "@/lib/server-words";
import { adminAreas } from "@/lib/session";
import { navigate, setLeaveGuard } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { IMPORT_RECORD, LEDE } from "@/lib/words";
import { cn, downloadText } from "@/lib/utils";

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
  // ask holds the departure the leave question waits on, from Cancel or
  // from the router's guard.
  const [ask, setAsk] = React.useState<{ go: () => void } | null>(null);
  const [testOpen, setTestOpen] = React.useState(false);
  const importBox = React.useRef<HTMLTextAreaElement>(null);
  const named = React.useRef(false);
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
  const importing = f.mode === "import";
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
    toast: liveNow(name),
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
    named.current = false;
    install.reset();
    setTestOpen(false);
    setAt(0);
    void loadApps();
  };

  const dirty = !!(name || f.desc.trim() || f.url.trim() || f.exec.trim() || f.image.trim());
  // away leaves for the servers list past the guard, once the person chose to.
  const away = () => { setLeaveGuard(null); navigate("servers"); };
  const cancel = () => { if (installed || dirty) setAsk({ go: away }); else navigate("servers"); };

  // While Cancel would ask, every other door out asks the same question: the
  // router's guard covers the breadcrumb, the rail, the header and the
  // palette. What next ends the wizard, and a Save draft that landed opens
  // its draft, which passes.
  const guarded = step !== "check" && (!!installed || dirty);
  const savingDraft = saver.busy === "draft";
  React.useEffect(() => {
    if (!guarded) return;
    setLeaveGuard((next, go) => {
      if (savingDraft && next.kind === "route" && next.key === "drafts") { go(); return; }
      setAsk({ go });
    });
    return () => setLeaveGuard(null);
  }, [guarded, savingDraft]);

  const label = name || "the server";
  const planned: Row[] = [
    { key: "install", label: publishRow(label), state: "pending" },
    ...(needsSecret(f) ? [{ key: "secret", label: "Store its secret", state: "pending" } as Row] : []),
    { key: "check", label: checkRow(label), state: "pending" },
  ];
  const spin = <Loader2Icon className="animate-spin" />;
  const push = <span className="flex-1" />;
  const forward = <Button onClick={() => void next()}>{nextTo(SERVER_STEPS[at + 1])}</Button>;

  let body: React.ReactNode = null;
  let footer: React.ReactNode = null;
  if (step === "server") {
    body = <ServerStep f={f} errors={errors} field={field} set={set} check={check} runtimes={runtimes} dry={dry} yaml={yaml} importBox={importBox} named={named} />;
    footer = <>{push}{forward}</>;
  } else if (step === "credential") {
    body = <CredentialStep f={f} errors={errors} control={form.control} field={field} set={set} providers={providers} installed={installed ? installed.name : ""} dry={dry} yaml={yaml} />;
    footer = installed
      ? <>{push}<Button onClick={() => void storeAgain()} disabled={install.busy}>{install.busy && spin}{STORE_AND_CHECK}</Button></>
      : <><Button variant="ghost" onClick={back}>{BACK}</Button>{push}{forward}</>;
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
    body = <CheckStep rows={landed.rows} read={landed.read} name={installed.name} tools={installed.tools || []} problem={install.problem} onOpen={open} />;
    footer = running ? (
      <>
        <Button variant="outline" onClick={() => downloadText(installed.name + ".yaml", yaml, "application/yaml")}><DownloadIcon />Download app.yaml</Button>
        {push}
        <Button variant="ghost" onClick={another}>Add another server</Button>
        <Button variant="outline" onClick={() => setTestOpen(true)}>Test a call</Button>
        {!whatNextShown(landed.rows, landed.read) && <Button onClick={open}>{openServer(installed.name)}</Button>}
      </>
    ) : (
      <>
        {landed.read && landed.read.kind === "refused" && needsSecret(f) && <Button variant="outline" onClick={() => setAt(1)} disabled={install.busy}>{SET_AGAIN}</Button>}
        <Button variant="outline" onClick={() => void install.recheck()} disabled={install.busy}><RefreshCwIcon className={cn(install.busy && "animate-spin")} />{RECHECK}</Button>
        {push}
        <Button variant="ghost" onClick={open}>{finishLater(installed.name)}</Button>
      </>
    );
  }

  if (locked) {
    return (
      <>
        <PageHead label={CRUMB_ADD} crumbs={[{ label: routeByKey("servers").label, to: "servers" }]} title="Add MCP server" description="" />
        <EmptyState icon={LockIcon} title="Add MCP server is not available to this account." action={<Button variant="outline" onClick={() => navigate("servers")}>Open MCP servers</Button>}>
          {REGISTER_NEEDS_GLOBAL}
        </EmptyState>
      </>
    );
  }

  return (
    <div className="flex min-h-full flex-col">
      <PageHead
        label={CRUMB_ADD}
        crumbs={[{ label: routeByKey("servers").label, to: "servers" }]}
        title={ADD_SERVER_TITLE}
        description=""
        actions={step !== "check" ? <Button variant="ghost" onClick={cancel} disabled={install.busy}>Cancel</Button> : undefined}
      />
      <div className="flex flex-1 flex-col gap-5 px-6 pt-5 pb-6">
        {/* What next ends the wizard, so every step reads as done there. */}
        <StepStrip labels={SERVER_STEPS} at={step === "check" && landed && whatNextShown(landed.rows, landed.read) ? SERVER_STEPS.length : at} />
        <div>
          <div className="flex flex-wrap items-baseline justify-between gap-x-4">
            <h2 ref={heading} tabIndex={-1} className="text-body leading-6 font-semibold text-foreground outline-none">{SERVER_STEPS[at]}</h2>
            {/* A registry record is not a way to reach a server, so its door sits beside the heading and opens the paste box in the step. */}
            {step === "server" && (
              <Button variant="link" size="sm" className="h-auto p-0" aria-expanded={importing} onClick={() => set({ mode: importing ? f.runtime : "import" })}>
                {IMPORT_RECORD}
              </Button>
            )}
          </div>
          {LEDE[step] && <p className="mt-0.5 max-w-[80ch] text-dense text-muted-foreground">{LEDE[step]}</p>}
        </div>
        {body}
      </div>
      <div className="sticky bottom-0 flex flex-wrap items-center gap-2 border-t border-border bg-background px-6 py-3" data-wizard-foot>{footer}</div>
      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{installed ? "Leave the wizard?" : "Discard these answers?"}</AlertDialogTitle>
            <AlertDialogDescription>
              {installed
                ? <><b className="font-semibold text-foreground">{installed.name}</b>{" is published and stays " + installed.status + "."}</>
                : "Nothing has been created yet. Discarding loses " + (name ? "the name " + name + " and " : "") + "the answers so far."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{installed ? "Keep going" : "Keep editing"}</AlertDialogCancel>
            <AlertDialogAction className={installed ? undefined : DANGER_FILL} onClick={() => { const go = ask?.go; setAsk(null); go?.(); }}>
              {installed ? "Leave" : "Discard the answers"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {installed && <TestACall open={testOpen} onOpenChange={setTestOpen} app={{ id: installed.id, name: installed.name }} tools={installed.tools || []} />}
    </div>
  );
}
