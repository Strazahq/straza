import * as React from "react";
import { DownloadIcon, Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { PageHead } from "@/components/page-head";
import { type SaveNote, SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { AccessStep, type Act, ComposeStep, DoneStep, type Draft, NameStep, PacksStep, type Pill, ReviewStep, planOf, storedName, toolNames } from "@/components/wizard/role-steps";
import { StepStrip } from "@/components/wizard/step-strip";
import { type Plan, emptyPlan, fixedFrom, grantMatchers, rulesFor } from "@/lib/access-plan";
import { mayWritePolicy } from "@/lib/access-read";
import { type ApiError, type AppRow, type PackRow, type PreviewEntry, type RoleRow, type ToolRow, bindPack, catalogPreview, exportRole, listApps, listPacks, listRoles, listTools } from "@/lib/api";
import { type GrantInput, planRows } from "@/lib/grant-commit";
import { take } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import {
  ADD_ANOTHER,
  BACK,
  CANCEL,
  DISCARD,
  DISCARD_TITLE,
  EXPORT_FAILED,
  KEEP_EDITING,
  type Kind,
  LEDE,
  NEXT,
  RAIL_TITLE,
  SECTION,
  SERVER_LEDE,
  SERVER_STEP,
  STEP,
  STEP_SUBJECT,
  SUBJECT_ROLES,
  UNREACHABLE_STEP,
  WIZARD_DESCRIPTION,
  WIZARD_TITLE,
  accessSetName,
  cliActivate,
  cliApply,
  cliCreate,
  cliImply,
  cliPack,
  discardBody,
  exportFile,
  lossComposed,
  lossName,
  lossPacks,
  liveRole,
  lossServer,
  missingRole,
  nameCheck,
  nameTaken,
  openRole,
  PICK_ANOTHER,
  roleFileName,
  rowCompose,
  rowCreate,
  rowCreateOwned,
  rowOn,
  rowPack,
} from "@/lib/role-words";
import { newRoleItems } from "@/lib/role-draft";
import { navigate } from "@/lib/router";
import { type RouteKey, routeByKey } from "@/lib/routes";
import { NOTHING_SAVED, SAVE_DRAFT, SAVE_PUBLISH } from "@/lib/save-words";
import { readFailed, refused } from "@/lib/say";
import { cliCreateRole } from "@/lib/server-roles-words";
import { downloadText } from "@/lib/utils";

// The New role wizard at roles/new: the kind, the one server an application role
// reaches and the name, then access to that server or the roles a business
// role composes, the packs its sessions receive, the review, and what
// landed. The Review ends in Save draft and Save and publish: the role, its
// access row, its rules and what it composes are one
// draft that goes live whole or not at all. A draft never applies a
// pack, so the packs are bound after the publish, and a pack that fails
// says where to finish it.

// DRAFT_NAME stands in for the role while a preview is asked before the
// name is typed: the preview takes a role name and the draft has none yet.
const DRAFT_NAME = "new-role";

// PREVIEW_MS lets the typing settle before the server is asked what the
// draft grant would reach.
const PREVIEW_MS = 300;

// REACHED are the preview statuses that mean the session gets the tool,
// gated or not, which is what a composed role's count says.
const REACHED = ["visible", "approve_gated", "hidden_policy"];

type StepKey = "name" | "access" | "compose" | "packs" | "review" | "done";

// Door is what a door into the wizard hands it: the server the new role
// reaches, from the last step of Add MCP server.
type Door = { server: string };

const EMPTY: Draft = { kind: "application", name: "", description: "", server: "", plans: {}, composed: [], packs: [] };

export function NewRole() {
  const route = routeByKey("roles");
  const [draft, setDraft] = React.useState<Draft>(EMPTY);
  const [step, setStep] = React.useState<StepKey>("name");
  const [roles, setRoles] = React.useState<RoleRow[] | null>(null);
  const [apps, setApps] = React.useState<AppRow[]>([]);
  const [tools, setTools] = React.useState<ToolRow[]>([]);
  const [packs, setPacks] = React.useState<PackRow[]>([]);
  const [appsProblem, setAppsProblem] = React.useState<string | null>(null);
  // railRead says the servers and their tools were both read, so the first
  // step never says no server serves a tool while the reads are on the way.
  const [railRead, setRailRead] = React.useState(false);
  const [previews, setPreviews] = React.useState<Record<string, Record<string, PreviewEntry>>>({});
  const [reach, setReach] = React.useState<Record<string, Record<string, number> | null>>({});
  const [miss, setMiss] = React.useState<"name" | "refused" | "server" | "tools" | null>(null);
  const [acts, setActs] = React.useState<Act[]>([]);
  // binding is the packs being bound after the publish, and reading the
  // own set a save starts from.
  const [binding, setBinding] = React.useState(false);
  const [reading, setReading] = React.useState<"draft" | "publish" | null>(null);
  const [problem, setProblem] = React.useState<SaveNote | null>(null);
  const [created, setCreated] = React.useState<RoleRow | null>(null);
  const [leave, setLeave] = React.useState<{ area: RouteKey; rest: string[] } | null>(null);
  // fromServer is the server the door handed over, which the first step names.
  const [fromServer, setFromServer] = React.useState("");
  const heading = React.useRef<HTMLHeadingElement>(null);
  const nameBox = React.useRef<HTMLInputElement>(null);
  const shown = React.useRef<StepKey>(step);
  const alive = React.useRef(true);

  const load = React.useCallback(() => {
    listRoles().then((r) => { if (alive.current) setRoles(r || []); }, () => { if (alive.current) setRoles(null); });
    const appsRead = listApps().then(
      (a) => { if (alive.current) { setApps(a || []); setAppsProblem(null); } },
      (e) => { if (alive.current) setAppsProblem(readFailed(RAIL_TITLE, e as ApiError)); },
    );
    const toolsRead = listTools().then((t) => { if (alive.current) setTools(t || []); }, () => undefined);
    void Promise.all([appsRead, toolsRead]).then(() => { if (alive.current) setRailRead(true); });
    listPacks().then((p) => { if (alive.current) setPacks(p || []); }, () => undefined);
  }, []);

  React.useEffect(() => {
    alive.current = true;
    load();
    return () => { alive.current = false; };
  }, [load]);

  // A door from a server opens on an application role with that server
  // picked on the first step, so the rail does not pick the first one
  // instead.
  React.useEffect(() => {
    const door = take<Door>("roles-new");
    if (!door) return;
    setFromServer(door.server);
    setDraft((d) => ({ ...d, kind: "application", server: door.server }));
  }, []);

  const toolsOf = React.useCallback((app: string) => tools.filter((t) => t.app === app), [tools]);
  const name = storedName(draft);
  const roleName = name || DRAFT_NAME;
  // owned says the role is an application role, which belongs to the one
  // server it reaches.
  const owned = draft.kind === "application";

  // The rail opens on the first server that serves tools, since a server
  // with none has nothing to tick.
  React.useEffect(() => {
    if (draft.server) return;
    const first = apps.find((a) => toolsOf(a.name).length > 0);
    if (first) setDraft((d) => (d.server ? d : { ...d, server: first.name }));
  }, [apps, draft.server, toolsOf]);

  // The previews are the server's own answer for the draft grant: assume makes
  // it truthful before the role exists. An empty tick list previews nothing.
  const current = apps.find((a) => a.name === draft.server) || null;
  const currentNames = current ? toolNames(toolsOf(current.name)) : [];
  const currentMatchers = current ? grantMatchers(planOf(draft, current.name), currentNames) : [];
  const matcherKey = currentMatchers.join(",");
  React.useEffect(() => {
    if (step !== "access" || !draft.server || !matcherKey) return undefined;
    const app = draft.server;
    const timer = setTimeout(() => {
      catalogPreview(roleName, app, matcherKey.split(",")).then(
        (answer) => {
          if (!alive.current) return;
          const byTool: Record<string, PreviewEntry> = {};
          for (const e of answer?.entries || []) if (e.tool && (e.status === "approve_gated" || e.status === "hidden_policy")) byTool[e.tool] = e;
          setPreviews((g) => ({ ...g, [app]: byTool }));
        },
        () => { if (alive.current) setPreviews((g) => ({ ...g, [app]: {} })); },
      );
    }, PREVIEW_MS);
    return () => clearTimeout(timer);
  }, [step, draft.server, matcherKey, roleName]);

  // What holders would reach arrives through the composed roles, so each
  // one is asked what its own sessions get, folded to a count per server.
  // Only an application role carries tools, so a composed admin role of a
  // server is not asked and shows no pill.
  const composedKey = draft.composed.filter((n) => (roles || []).some((r) => r.name === n && r.kind === "application")).join(",");
  React.useEffect(() => {
    for (const other of composedKey ? composedKey.split(",") : []) {
      if (reach[other] !== undefined) continue;
      catalogPreview(other).then(
        (answer) => {
          if (!alive.current) return;
          const counts: Record<string, number> = {};
          for (const e of answer?.entries || []) if (e.app && REACHED.includes(e.status)) counts[e.app] = (counts[e.app] || 0) + 1;
          setReach((p) => ({ ...p, [other]: counts }));
        },
        () => { if (alive.current) setReach((p) => ({ ...p, [other]: null })); },
      );
    }
  }, [composedKey, reach]);

  const steps: StepKey[] = React.useMemo(() => {
    const body: StepKey[] = draft.kind === "application" ? ["access"] : draft.kind === "business" ? ["compose"] : [];
    const withPacks: StepKey[] = packs.length && draft.kind !== "approver" ? ["packs"] : [];
    return (["name"] as StepKey[]).concat(body, withPacks, ["review", "done"]);
  }, [draft.kind, packs.length]);
  const at = Math.max(0, steps.indexOf(step));
  // label is a step's name in the strip and its heading: an application
  // role's first step also picks its server.
  const label = (s: StepKey) => (s === "name" && owned ? SERVER_STEP : STEP[s]);

  // A new step moves focus to its heading, so a keyboard user starts there
  // and not on a button that left with the old step.
  React.useEffect(() => {
    if (shown.current === step) return;
    shown.current = step;
    heading.current?.focus();
  }, [step]);

  // pickedApps is the one server the run writes: the picked one, when its
  // plan grants a tool. Ticks on any other server stay in the draft, and
  // so do the ticks of a role whose kind moved off Application role.
  const pickedApps = owned ? apps.filter((a) => a.name === draft.server && grantMatchers(planOf(draft, a.name), toolNames(toolsOf(a.name))).length > 0) : [];
  const packName = (id: string) => packs.find((p) => p.id === id)?.name || id;
  // The composed roles are resolved against the same read the Compose step
  // offers, since the implication is written by the role's id.
  const composed = draft.composed.map((n) => (roles || []).find((r) => r.name === n)).filter((r): r is RoleRow => !!r);

  const inputFor = (app: AppRow): GrantInput => ({ role: { name }, app, tools: toolsOf(app.name), plan: planOf(draft, app.name) });
  // rules says whether the save writes rules into the role's own set.
  const rules = pickedApps.some((a) => rulesFor(planOf(draft, a.name), a.name, toolNames(toolsOf(a.name)), roleName).length > 0);
  const ownSet = accessSetName(name);
  // fixed are the rules of other sets the preview says decide a tool of
  // the picked server; the editor and the review show them in their words.
  const fixed = fixedFrom(previews[draft.server], accessSetName(roleName), null);

  // planned is the run before it runs: the create, then each server's own
  // rows, the compositions and the packs, in the order they are written.
  const planned = (): Act[] => {
    const out: Act[] = [{ id: "create", label: owned ? rowCreateOwned(name, draft.server) : rowCreate(name), state: "pending", subject: STEP_SUBJECT.create }];
    for (const app of pickedApps) {
      for (const r of planRows(inputFor(app))) {
        out.push({
          id: r.key + ":" + app.name,
          label: r.key === "grant" ? rowOn(r.label, app.name) : r.label,
          state: "pending",
          section: r.key === "publish" ? SECTION.publish : SECTION.bind,
          subject: STEP_SUBJECT[r.key],
        });
      }
    }
    for (const other of composed) out.push({ id: "imply:" + other.name, label: rowCompose(other.name), state: "pending", section: SECTION.imply, subject: STEP_SUBJECT.imply });
    for (const id of draft.packs) out.push({ id: "pack:" + id, label: rowPack(packName(id)), state: "pending", section: SECTION.pack, subject: STEP_SUBJECT.pack });
    return out;
  };

  // An application role is made with its server and its tools in one
  // line, the line the Add role sheet of a server shows.
  const commands = [
    owned ? cliCreateRole(name, draft.server, currentMatchers, draft.description.trim()) : cliCreate(name, draft.kind, draft.description.trim()),
    ...(rules ? [cliApply(ownSet), cliActivate(ownSet)] : []),
    ...composed.map((other) => cliImply(name, other.name)),
    ...draft.packs.map((id) => cliPack(packName(id), name)),
  ].join("\n");

  const setPlan = (app: string) => (update: (p: Plan) => Plan) => {
    setMiss(null);
    setDraft((d) => ({ ...d, plans: { ...d.plans, [app]: update(d.plans[app] || emptyPlan()) } }));
  };

  const toggle = (key: "composed" | "packs", value: string) =>
    setDraft((d) => ({ ...d, [key]: d[key].includes(value) ? d[key].filter((x) => x !== value) : d[key].concat(value) }));

  // next keeps the primary clickable, and a click
  // that cannot move on focuses the answer it needs and says what is
  // missing under it. An application role needs its server first, and the
  // server refuses one that reaches no tool.
  const next = () => {
    if (step === "name") {
      if (owned && !current) { setMiss("server"); return; }
      if (!name) { setMiss("name"); nameBox.current?.focus(); return; }
      const check = nameCheck(name, roles);
      if (check && check.level === "error") { setMiss("refused"); nameBox.current?.focus(); return; }
    }
    if (step === "access" && !currentMatchers.length) { setMiss("tools"); return; }
    setMiss(null);
    setStep(steps[Math.min(at + 1, steps.length - 1)]);
  };
  const back = () => setStep(steps[Math.max(at - 1, 0)]);

  // bindPacks runs after the publish: every row but the packs landed with
  // it, and each pack is bound to the new role in order, stopping at the
  // first failure, which leaves the earlier ones bound.
  const bindPacks = async () => {
    let rows = planned().map((a) => (a.section === SECTION.pack ? a : { ...a, state: "done" as const }));
    setActs(rows);
    setStep("done");
    const put = (id: string, patch: Partial<Act>) => {
      rows = rows.map((a) => (a.id === id ? { ...a, ...patch } : a));
      setActs(rows);
    };
    const sentence = (e: unknown) => {
      const err = e as ApiError;
      return err.unreachable ? UNREACHABLE_STEP : refused(err);
    };
    setBinding(true);
    let role: RoleRow | undefined;
    try {
      role = (await listRoles()).find((r) => r.name === name);
    } catch (e) {
      if (draft.packs.length) put("pack:" + draft.packs[0], { state: "failed", error: readFailed(SUBJECT_ROLES, e as ApiError) });
      setBinding(false);
      return;
    }
    if (!role) {
      if (draft.packs.length) put("pack:" + draft.packs[0], { state: "failed", error: missingRole(name) });
      setBinding(false);
      return;
    }
    setCreated(role);
    for (const packID of draft.packs) {
      const id = "pack:" + packID;
      put(id, { state: "running" });
      try {
        await bindPack(packID, role.id);
      } catch (e) {
        put(id, { state: "failed", error: sentence(e) });
        break;
      }
      put(id, { state: "done" });
    }
    setBinding(false);
  };

  // A draft put of a live role's name changes that role, so a check that
  // answers the role as live refuses the save in the Name step's words.
  const save = useDraftSave({ name, toast: liveRole(name), creates: { object: "Role/" + name, taken: nameTaken(name) + " " + PICK_ANOTHER }, onPublished: () => void bindPacks() });
  const busy = binding || reading !== null || save.busy !== null;

  // send builds the one draft from the answers and hands it to the save:
  // the role with its access row and what it composes, and its own set
  // when the grant writes rules.
  const send = async (publish: boolean) => {
    if (busy) return;
    setProblem(null);
    setReading(publish ? "publish" : "draft");
    const app = pickedApps[0];
    const built = await newRoleItems({ name, kind: draft.kind, description: draft.description.trim(), implies: composed.map((r) => r.name) }, app ? inputFor(app) : null);
    setReading(null);
    if ("error" in built) { setProblem({ tone: "refused", lines: [NOTHING_SAVED, built.error] }); return; }
    await (publish ? save.saveAndPublish(built.items) : save.saveDraft(built.items));
  };

  const exportYAML = async () => {
    if (!created) return;
    try {
      downloadText(roleFileName(created.name), await exportRole(created.id), "application/yaml");
    } catch (e) {
      notify.failed(readFailed(EXPORT_FAILED, e as ApiError));
    }
  };

  const another = () => {
    setDraft(EMPTY);
    setActs([]);
    setCreated(null);
    setProblem(null);
    setPreviews({});
    setReach({});
    setMiss(null);
    setStep("name");
    load();
  };

  // The discard gate names what leaving would lose. An empty draft has
  // nothing to lose, so it leaves without a question.
  const servers = pickedApps.length;
  const dirty = !!(name || draft.description.trim() || servers || draft.composed.length || draft.packs.length);
  const bits = [
    ...(name ? [lossName(name)] : []),
    ...(servers ? [lossServer(draft.server)] : []),
    ...(draft.composed.length ? [lossComposed(draft.composed.length)] : []),
    ...(draft.packs.length ? [lossPacks(draft.packs.length)] : []),
  ];
  // exit leaves the wizard for another page, the role the name is taken by
  // or the server whose prefix it takes, past the discard question.
  const exit = (rest: string[], area: RouteKey = "roles") => { if (dirty) setLeave({ area, rest }); else navigate(area, rest); };

  const pills: Pill[] = [];
  let unread = false;
  for (const other of draft.composed) {
    const counts = reach[other];
    if (counts === null) { unread = true; continue; }
    for (const server of Object.keys(counts || {}).sort()) pills.push({ server, via: other, tools: counts[server] });
  }

  const failed = acts.find((a) => a.state === "failed") || null;
  const done = step === "done";
  const spin = <Loader2Icon className="animate-spin" />;
  const push = <span className="flex-1" />;

  let body: React.ReactNode = null;
  if (step === "name") {
    body = (
      <NameStep
        draft={draft}
        roles={roles}
        apps={apps}
        read={railRead}
        toolsOf={toolsOf}
        problem={appsProblem}
        fromServer={fromServer}
        miss={miss === "tools" ? null : miss}
        nameBox={nameBox}
        onKind={(kind: Kind) => { setMiss(null); setDraft((d) => ({ ...d, kind })); }}
        onPick={(server) => { setMiss(null); setDraft((d) => ({ ...d, server })); }}
        onName={(v) => { setMiss(null); setDraft((d) => ({ ...d, name: v })); }}
        onDescription={(v) => setDraft((d) => ({ ...d, description: v }))}
        onOpenExact={(r) => exit([r.id])}
        onOpenServer={(a) => exit([a.id], "servers")}
      />
    );
  } else if (step === "access" && current) {
    body = (
      <AccessStep
        draft={draft}
        app={current}
        toolsOf={toolsOf}
        onPlan={setPlan(draft.server)}
        fixed={fixed}
        approvers={(roles || []).filter((r) => r.kind === "approver")}
        roleName={roleName}
        readOnly={!mayWritePolicy()}
        missing={miss === "tools"}
      />
    );
  } else if (step === "compose") {
    body = <ComposeStep draft={draft} roles={roles || []} pills={pills} unread={unread} onToggle={(n) => toggle("composed", n)} />;
  } else if (step === "packs") {
    body = <PacksStep draft={draft} packs={packs} onToggle={(id) => toggle("packs", id)} />;
  } else if (step === "review") {
    const note = problem || save.note;
    body = (
      <>
        <ReviewStep draft={draft} apps={apps} toolsOf={toolsOf} fixed={fixed} packs={packs} hadPacks={steps.includes("packs")} acts={planned()} commands={commands} />
        {note && <SaveNoteLine note={note} className="max-w-[80ch]" />}
      </>
    );
  } else {
    body = <DoneStep acts={acts} name={name} failed={failed} running={binding} />;
  }

  let footer: React.ReactNode = null;
  if (step === "name") {
    footer = <>{push}<Button onClick={next}>{NEXT}</Button></>;
  } else if (step === "review") {
    footer = (
      <>
        <Button variant="ghost" onClick={back} disabled={busy}>{BACK}</Button>
        {push}
        <Button variant="outline" onClick={() => void send(false)} disabled={busy} data-role-draft>{(reading === "draft" || save.busy === "draft") && spin}{SAVE_DRAFT}</Button>
        <Button onClick={() => void send(true)} disabled={busy} data-role-publish>{(reading === "publish" || save.busy === "publish") && spin}{SAVE_PUBLISH}</Button>
      </>
    );
  } else if (!done) {
    footer = (
      <>
        <Button variant="ghost" onClick={back}>{BACK}</Button>
        {push}
        <Button onClick={next}>{NEXT}</Button>
      </>
    );
  } else if (created && !failed && !busy) {
    footer = (
      <>
        <Button variant="outline" onClick={() => void exportYAML()}><DownloadIcon />{exportFile(created.name)}</Button>
        {push}
        <Button variant="ghost" onClick={another}>{ADD_ANOTHER}</Button>
        <Button onClick={() => navigate("roles", [created.id])}>{openRole(created.name)}</Button>
      </>
    );
  } else if (created && !busy) {
    footer = <>{push}<Button onClick={() => navigate("roles", [created.id])}>{openRole(created.name)}</Button></>;
  }

  return (
    <div className="flex min-h-full flex-col">
      <PageHead
        label={route.label}
        title={WIZARD_TITLE}
        description={WIZARD_DESCRIPTION}
        actions={done ? undefined : <Button variant="ghost" onClick={() => exit([])}>{CANCEL}</Button>}
      />
      <div className="flex flex-1 flex-col gap-5 px-6 pt-5 pb-6">
        <StepStrip labels={steps.map(label)} at={at} />
        <div>
          <h2 ref={heading} tabIndex={-1} className="text-base font-semibold text-foreground outline-none">{label(step)}</h2>
          <p className="mt-0.5 flex max-w-[80ch] flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
            {step === "name" && owned ? SERVER_LEDE : LEDE[step]}
          </p>
        </div>
        {body}
      </div>
      <div className="sticky bottom-0 flex items-center gap-2 border-t border-border bg-background px-6 py-3" data-wizard-foot>{footer}</div>
      {save.dialog}

      <AlertDialog open={!!leave} onOpenChange={(o) => { if (!o) setLeave(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-discard-dialog>
          <AlertDialogHeader>
            <AlertDialogTitle>{DISCARD_TITLE}</AlertDialogTitle>
            <AlertDialogDescription>{discardBody(bits)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{KEEP_EDITING}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={() => navigate(leave ? leave.area : "roles", leave ? leave.rest : [])}>{DISCARD}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
