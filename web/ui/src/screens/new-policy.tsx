import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { FetchError, RefusedError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { PolicyPublish } from "@/components/policy-publish";
import { SaveNoteLine, useDraftSave } from "@/components/use-draft-save";
import { type Draft, type Intent, type Lane, type Verdict, CallsStep, EMPTY_DRAFT, HowStep, ReviewStep, WhatStep, WhoStep, nameProblem, pickedNames, reasonDefault, ruleIdOf, ruleOf, viewOf } from "@/components/wizard/policy-steps";
import { StepStrip } from "@/components/wizard/step-strip";
import {
  type ApiError, type AppRow, type BindingRow, type PoliciesAnswer, type PolicySetRow, type PreviewEntry, type RoleRow, type SimulateRequest, type ToolRow,
  applyPolicy, catalogPreview, getPolicy, listApps, listBindings, listPolicies, listRoles, listTools, listUsers, query, validatePolicy,
} from "@/lib/api";
import { addRule, docText, newPolicyDoc, openDoc, readSet, ruleView, rulesOf, uniqueId } from "@/lib/policy-model";
import {
  BACK, CALLS_MISSING, CANCEL, EDITOR_LABEL, LEAVE, LEAVE_BODY, LEAVE_TITLE, NAME_BAD, NAME_TAKEN, NEXT, STAY, STEPS,
  SUBJECT_POLICIES, SUBJECT_ROLES, SUBJECT_SERVERS, WHO_MISSING, WIZ_LEDE, WIZ_TITLE, WRITE_YAML_LEDE, WRITE_YAML_START, WRITE_YAML_TITLE,
  probeCall, savedToast, sentence, subjectWords, suggestedName,
} from "@/lib/policy-words";
import { notify } from "@/lib/notify";
import { take, put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { SAVE_DRAFT, SAVE_PUBLISH } from "@/lib/save-words";
import { checkFailed, readFailed, refused } from "@/lib/say";
import { matchesTool } from "@/lib/words";

// The New policy wizard at policies/new:
// what should happen, whose sessions it governs, which calls it matches,
// how long a person has to decide, and the review that stores it. Nothing
// reaches the server before Save as draft or Publish.

const route = routeByKey("policies");

// VALIDATE_MS lets the typing settle before the server is asked what it
// makes of the document.
const VALIDATE_MS = 400;

// HOLDERS is how many holders are read for the review's Applies to fact;
// the sentence names two of them.
const HOLDERS = 3;

type StepKey = "what" | "who" | "calls" | "how" | "review";

// Door is what the list and the By role view hand over: the role and the
// intent already chosen, and the lane when the Lane filter named one.
type Door = { role?: string | null; intent?: Intent; lane?: string };

const settled = <T,>(r: PromiseSettledResult<T>): T | null => (r.status === "fulfilled" ? r.value : null);

export function NewPolicy() {
  const [draft, setDraft] = React.useState<Draft>(EMPTY_DRAFT);
  const [step, setStep] = React.useState<StepKey>("what");
  const [allRoles, setAllRoles] = React.useState<RoleRow[]>([]);
  const [rolesProblem, setRolesProblem] = React.useState<string | null>(null);
  const [apps, setApps] = React.useState<AppRow[]>([]);
  const [tools, setTools] = React.useState<ToolRow[]>([]);
  const [bindings, setBindings] = React.useState<BindingRow[]>([]);
  const [catalogProblem, setCatalogProblem] = React.useState<string | null>(null);
  const [sets, setSets] = React.useState<PolicySetRow[]>([]);
  const [setsProblem, setSetsProblem] = React.useState<string | null>(null);
  const [preview, setPreview] = React.useState<Record<string, Record<string, PreviewEntry>>>({});
  const [people, setPeople] = React.useState<string[] | null>(null);
  const [verdict, setVerdict] = React.useState<Verdict | null>(null);
  const [stored, setStored] = React.useState<string | null>(null);
  const [miss, setMiss] = React.useState<string | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [publishing, setPublishing] = React.useState(false);
  const [leaving, setLeaving] = React.useState(false);
  const [yamlOpen, setYamlOpen] = React.useState(false);
  const [yamlText, setYamlText] = React.useState(WRITE_YAML_START);
  const [yamlRefusal, setYamlRefusal] = React.useState<string | null>(null);
  const [yamlSaving, setYamlSaving] = React.useState(false);
  // The two Save draft doors of the wizard, the review's and the Write YAML
  // sheet's, each with the note it shows beside its buttons.
  const reviewSave = useDraftSave({ name: "", onPublished: () => undefined });
  const yamlSave = useDraftSave({ name: "", onPublished: () => undefined });
  const nameBox = React.useRef<HTMLInputElement>(null);
  const body = React.useRef<HTMLDivElement>(null);

  // A door from the list answers the steps it knows, so the wizard opens
  // on the first question that is still open.
  React.useEffect(() => {
    const door = take<Door>("policies-new");
    if (!door) return;
    const lane = door.lane as Lane | undefined;
    setDraft((d) => ({
      ...d,
      intent: door.intent || d.intent,
      role: door.role === undefined ? d.role : door.role,
      lane: lane && ["mcp", "shell", "files", "net"].includes(lane) ? lane : d.lane,
    }));
    setStep(door.intent && door.role !== undefined ? "calls" : door.intent ? "who" : "what");
  }, []);

  React.useEffect(() => {
    let alive = true;
    void Promise.allSettled([listRoles(), listApps(), listTools(), listBindings(), listPolicies(query({ limit: 0 }))]).then(
      ([rolesR, appsR, toolsR, bindingsR, setsR]) => {
        if (!alive) return;
        const read = settled<RoleRow[]>(rolesR);
        setAllRoles(read || []);
        if (!read) setRolesProblem(readFailed(SUBJECT_ROLES, (rolesR as PromiseRejectedResult).reason as ApiError));
        setApps(settled<AppRow[]>(appsR) || []);
        setTools(settled<ToolRow[]>(toolsR) || []);
        setBindings(settled<BindingRow[]>(bindingsR) || []);
        if (appsR.status === "rejected") setCatalogProblem(readFailed(SUBJECT_SERVERS, appsR.reason as ApiError));
        const answer = settled<PoliciesAnswer>(setsR);
        setSets((answer && answer.items) || []);
        if (!answer) setSetsProblem(readFailed(SUBJECT_POLICIES, (setsR as PromiseRejectedResult).reason as ApiError));
      },
    );
    return () => { alive = false; };
  }, []);

  // filled is the draft as the surfaces read it: the offered reason and
  // the suggested name stand in until the person writes their own.
  const filled: Draft = React.useMemo(
    () => ({
      ...draft,
      reason: draft.reasonTyped ? draft.reason : reasonDefault(draft),
      name: draft.nameTyped ? draft.name : suggestedName(draft.role === null ? null : draft.role || null, draft.intent),
    }),
    [draft],
  );

  // A policy names application roles; the approver kinds only decide, so
  // they are offered on the How step alone.
  const roles = React.useMemo(() => allRoles.filter((r) => r.kind === "application").sort((a, b) => a.name.localeCompare(b.name)), [allRoles]);
  const appNames = React.useMemo(() => apps.map((a) => a.name).sort(), [apps]);
  const reached = React.useCallback((role: string) => [...new Set(bindings.filter((b) => b.role === role).map((b) => b.app))].sort(), [bindings]);
  const servers = draft.role ? reached(draft.role) : appNames;
  const toolNames = React.useMemo(() => tools.filter((t) => t.app === draft.app).map((t) => t.name).sort(), [tools, draft.app]);
  const reaches = (tool: string) => (draft.role ? bindings.some((b) => b.role === draft.role && b.app === draft.app && matchesTool(b.tools, tool)) : true);

  // The Server picker opens on the first server the role reaches, so the
  // step has something to say the moment it is on screen.
  React.useEffect(() => {
    if (draft.lane !== "mcp" || draft.app || servers.length === 0) return;
    setDraft((d) => ({ ...d, app: servers[0] }));
  }, [draft.lane, draft.app, servers]);

  // What each tool does today comes from the server's own preview: for a
  // role its own answer, for Everyone the answer of each role that reaches
  // the server.
  const previewKey = draft.lane === "mcp" && draft.app ? (draft.role === null ? "*" : draft.role || "") + "|" + draft.app : "";
  React.useEffect(() => {
    if (!previewKey || preview[previewKey]) return;
    const app = draft.app;
    const who = draft.role ? [draft.role] : [...new Set(bindings.filter((b) => b.app === app).map((b) => b.role))];
    if (!who.length) return;
    let alive = true;
    void Promise.allSettled(who.map((r) => catalogPreview(r, app))).then((answers) => {
      if (!alive) return;
      const byTool: Record<string, PreviewEntry> = {};
      for (const answer of answers) {
        if (answer.status !== "fulfilled") continue;
        for (const e of (answer.value && answer.value.entries) || []) {
          if (!e.tool) continue;
          // A visible entry wins, so Everyone reads that a tool runs when
          // any role reaches it.
          if (!byTool[e.tool] || e.status === "visible") byTool[e.tool] = e;
        }
      }
      setPreview((p) => ({ ...p, [previewKey]: byTool }));
    });
    return () => { alive = false; };
  }, [previewKey, draft.app, draft.role, bindings, preview]);

  // The review names who holds the role today.
  React.useEffect(() => {
    if (step !== "review" || !draft.role || people) return;
    let alive = true;
    listUsers(query({ role: draft.role, limit: HOLDERS })).then(
      (page) => { if (alive) setPeople((page.items || []).map((u) => u.username).slice(0, 2)); },
      () => undefined,
    );
    return () => { alive = false; };
  }, [step, draft.role, people]);

  // exists is the live set that already matches exactly this role, offered
  // on the review as the home for the new rules.
  const exists = React.useMemo(() => {
    if (!draft.role) return null;
    const found = sets.find((s) => {
      const summary = s.summary;
      const matched = (summary && summary.matchRoles) || [];
      return s.status === "active" && matched.length === 1 && matched[0] === draft.role && !(summary && summary.matchOther);
    });
    return found ? found.name : null;
  }, [sets, draft.role]);

  // The stored text of the set the rules are added to arrives before the
  // review can show the merged document.
  React.useEffect(() => {
    if (!draft.into) { setStored(null); return; }
    let alive = true;
    getPolicy(draft.into).then((doc) => { if (alive) setStored(doc.yaml || ""); }, () => { if (alive) setStored(null); });
    return () => { alive = false; };
  }, [draft.into]);

  const intoRow = draft.into ? sets.find((s) => s.name === draft.into) || null : null;
  const storedName = draft.into || filled.name;

  // text is the document as it will be stored: a new policy, or the set it
  // is added to with the new rule appended and its comments kept.
  const text = React.useMemo(() => {
    const base = ruleIdOf(filled) || "new-rule";
    if (draft.into) {
      if (stored === null) return "";
      try {
        const doc = openDoc(stored);
        addRule(doc, ruleOf(filled, uniqueId(base, rulesOf(doc).map((r) => r.id))));
        return docText(doc);
      } catch {
        return stored;
      }
    }
    const rule = ruleOf(filled, base);
    return docText(newPolicyDoc({
      name: filled.name,
      description: sentence(ruleView(rule, 0)),
      priority: draft.role ? 150 : 100,
      roles: draft.role ? [draft.role] : [],
      rules: [rule],
    }));
  }, [filled, draft.into, draft.role, stored]);

  // The server's own check runs on the review and after every change to
  // the document, so a refusal is read here and never at publish.
  React.useEffect(() => {
    if (step !== "review" || !text) return;
    const timer = setTimeout(() => {
      validatePolicy(text).then(
        (ok) => setVerdict({ ok }),
        (e) => {
          const err = e as ApiError;
          if (err.status !== 401) setVerdict({ bad: checkFailed(err) });
        },
      );
    }, VALIDATE_MS);
    return () => clearTimeout(timer);
  }, [step, text]);

  const steps: StepKey[] = draft.intent === "allow" ? ["what", "who", "calls", "review"] : ["what", "who", "calls", "how", "review"];
  const at = Math.max(0, steps.indexOf(step));
  const taken = sets.map((s) => s.name);
  const named = pickedNames(filled);
  const hasCalls = draft.lane === "net" || (draft.lane === "mcp" ? !!draft.app && (draft.wholeServer || named.length > 0) : named.length > 0);

  const change = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));

  // focusMissing puts the cursor on the first control of the step that
  // still needs an answer.
  const focusMissing = () => body.current?.querySelector<HTMLElement>("input, textarea, [role=radio], [role=combobox]")?.focus();

  const next = () => {
    // An empty role name means the role option was chosen and no card
    // picked, which is as unanswered as choosing nothing.
    if (step === "who" && (draft.role === undefined || draft.role === "")) { setMiss(WHO_MISSING); focusMissing(); return; }
    if (step === "calls" && !hasCalls) { setMiss(CALLS_MISSING); focusMissing(); return; }
    setMiss(null);
    setStep(steps[Math.min(at + 1, steps.length - 1)]);
  };
  const back = () => { setMiss(null); setStep(steps[Math.max(at - 1, 0)]); };

  // nameBlocked is the reason the review cannot store the document under
  // this name, and it focuses the field that carries it.
  const nameBlocked = (): boolean => {
    if (draft.into) return false;
    const problem = nameProblem(filled.name, taken);
    if (!problem) return false;
    setMiss(problem === "taken" ? NAME_TAKEN : NAME_BAD);
    nameBox.current?.focus();
    return true;
  };

  // saveTo is Save draft for a document named name. A set that exists
  // keeps it as that set's saved edit through the policy route, as the
  // set's page does, and a new set joins the person's working draft, so no
  // set is stored before a publish.
  const saveTo = async (name: string, doc: string, save: typeof reviewSave, refuse: (s: string | null) => void, busy: (b: boolean) => void) => {
    const row = sets.find((r) => r.name === name);
    if (!row) { await save.saveDraft([{ kind: "PolicySet", name, op: "put", doc }]); return; }
    refuse(null);
    busy(true);
    try {
      await applyPolicy(doc);
      notify.ok(savedToast(name, row.status === "active"));
      navigate("policies", [name]);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) refuse(refused(err));
    } finally {
      busy(false);
    }
  };

  const saveDraft = async () => {
    if (saving || reviewSave.busy || nameBlocked()) return;
    setMiss(null);
    await saveTo(storedName, text, reviewSave, setRefusal, setSaving);
  };

  // saveYAML reads the name the written text gives, and the server's check
  // answers a text that gives none.
  const saveYAML = async () => {
    if (yamlSaving || yamlSave.busy) return;
    let name = "";
    try { name = readSet(openDoc(yamlText)).name; } catch { name = ""; }
    await saveTo(name, yamlText, yamlSave, setYamlRefusal, setYamlSaving);
  };

  // probe is the one call the publish dialog reads before and after: the
  // first tool the rule names, made by a holder of the role.
  const probeUser = people && people.length ? people[0] : "";
  const probe: { event: SimulateRequest["event"]; user?: string; label: string } | undefined = React.useMemo(() => {
    if (!probeUser) return undefined;
    const one = { ...filled, wholeServer: false, tools: filled.tools.slice(0, 1), patterns: filled.patterns.slice(0, 1), paths: filled.paths.slice(0, 1) };
    const tool = draft.lane === "mcp" ? one.tools[0] || toolNames[0] || "" : "";
    if (draft.lane === "mcp" && (!tool || !draft.app)) return undefined;
    const event: SimulateRequest["event"] =
      draft.lane === "mcp" ? { kind: "tool.pre", tool: "mcp.call", app: draft.app, toolName: tool }
        : draft.lane === "shell" ? { kind: "tool.pre", tool: "shell.exec", command: one.patterns[0] || "" }
          : draft.lane === "files" ? { kind: "tool.pre", tool: "file.write", paths: [one.paths[0] || ""] }
            : { kind: "tool.pre", tool: "net.fetch" };
    const call = draft.lane === "shell" ? one.patterns[0] || "" : draft.lane === "files" ? one.paths[0] || "" : subjectWords(viewOf({ ...one, tools: tool ? [tool] : [] }));
    return { event, user: probeUser, label: probeCall(call, probeUser) };
  }, [filled, probeUser, draft.lane, draft.app, toolNames]);

  const dirty = at > 0 || draft.role !== undefined || named.length > 0;
  const leave = () => { if (dirty) setLeaving(true); else navigate("policies"); };

  const spin = <Loader2Icon className="animate-spin" />;

  let inner: React.ReactNode = null;
  if (step === "what") {
    inner = <WhatStep intent={draft.intent} onIntent={(intent) => change({ intent })} onWriteYAML={() => setYamlOpen(true)} />;
  } else if (step === "who") {
    inner = <WhoStep draft={draft} roles={roles} problem={rolesProblem} serversOf={reached} onRole={(role) => { setMiss(null); change({ role, app: "", tools: [] }); }} />;
  } else if (step === "calls") {
    inner = (
      <CallsStep
        draft={draft}
        servers={servers}
        totalServers={appNames.length}
        tools={toolNames}
        preview={preview[previewKey] || {}}
        reaches={reaches}
        onChange={(patch) => { setMiss(null); change(patch); }}
      />
    );
  } else if (step === "how") {
    inner = <HowStep draft={filled} approvers={approversOf(allRoles)} onChange={change} />;
  } else {
    inner = (
      <ReviewStep
        draft={filled}
        taken={taken}
        exists={exists}
        people={people}
        yaml={text}
        verdict={verdict}
        nameBox={nameBox}
        onName={(name) => { setMiss(null); change({ name, nameTyped: true }); }}
        onInto={(into) => { setMiss(null); change({ into }); }}
      />
    );
  }

  return (
    <div className="flex min-h-full flex-col">
      <PageHead
        label={route.label}
        title={WIZ_TITLE}
        description={WIZ_LEDE}
        actions={<Button variant="ghost" onClick={leave}>{CANCEL}</Button>}
      />
      <div className="flex flex-1 flex-col gap-5 px-6 pt-5 pb-6">
        <StepStrip labels={steps.map((s) => STEPS[s])} at={at} />
        {setsProblem && <FetchError subject={SUBJECT_POLICIES} detail={setsProblem} />}
        {catalogProblem && step === "calls" && <FetchError subject={SUBJECT_SERVERS} detail={catalogProblem} />}
        <div ref={body}>{inner}</div>
        {refusal && <RefusedError subject={SAVE_DRAFT} message={refusal} />}
        {reviewSave.note && <SaveNoteLine note={reviewSave.note} />}
      </div>

      <div className="sticky bottom-0 flex flex-col gap-1 border-t border-border bg-background px-6 py-3" data-wizard-foot>
        <div className="flex items-center gap-2">
          {at > 0 && <Button variant="ghost" onClick={back}>{BACK}</Button>}
          <span className="flex-1" />
          {step === "review" && <Button variant="outline" onClick={() => void saveDraft()} disabled={saving || reviewSave.busy !== null}>{(saving || reviewSave.busy) && spin}{SAVE_DRAFT}</Button>}
          {step === "review"
            ? <Button onClick={() => { if (!nameBlocked()) { setMiss(null); setPublishing(true); } }} data-open-publish>{SAVE_PUBLISH}</Button>
            : <Button onClick={next}>{NEXT}</Button>}
        </div>
        {miss && <p className="text-right text-[13px] text-danger" data-missing>{miss}</p>}
      </div>

      <PolicyPublish
        open={publishing}
        onOpenChange={setPublishing}
        name={storedName}
        text={text}
        baseText={draft.into ? stored : null}
        wasLive={!!intoRow && intoRow.status === "active"}
        roles={draft.into ? (intoRow && intoRow.summary && intoRow.summary.matchRoles) || [] : draft.role ? [draft.role] : []}
        probe={probe}
        onDone={(result) => {
          put("policies-proof", { name: result.name, snapshot: result.snapshot, at: new Date().toISOString() });
          navigate("policies", [result.name]);
        }}
      />

      <Sheet open={yamlOpen} onOpenChange={setYamlOpen}>
        <SheetContent className="w-full gap-0 p-0 sm:max-w-[680px]" data-write-yaml>
          <SheetHeader className="border-b border-border pr-12">
            <SheetTitle className="text-lg leading-snug">{WRITE_YAML_TITLE}</SheetTitle>
            <SheetDescription>{WRITE_YAML_LEDE}</SheetDescription>
          </SheetHeader>
          <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4">
            <Textarea
              aria-label={EDITOR_LABEL}
              spellCheck={false}
              value={yamlText}
              onChange={(e) => setYamlText(e.target.value)}
              className="h-[420px] resize-none whitespace-pre font-mono text-[13px]"
            />
            {yamlRefusal && <RefusedError subject={SAVE_DRAFT} message={yamlRefusal} />}
            {yamlSave.note && <SaveNoteLine note={yamlSave.note} />}
          </div>
          <SheetFooter className="flex-row justify-end border-t border-border">
            <Button variant="outline" onClick={() => setYamlOpen(false)} disabled={yamlSaving}>{CANCEL}</Button>
            <Button onClick={() => void saveYAML()} disabled={yamlSaving || yamlSave.busy !== null} data-save-yaml>{(yamlSaving || yamlSave.busy) && spin}{SAVE_DRAFT}</Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <AlertDialog open={leaving} onOpenChange={setLeaving}>
        <AlertDialogContent className="sm:max-w-[560px]" data-leave-dialog>
          <AlertDialogHeader>
            <AlertDialogTitle>{LEAVE_TITLE}</AlertDialogTitle>
            <AlertDialogDescription>{LEAVE_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{STAY}</AlertDialogCancel>
            <AlertDialogAction
              className="border border-danger/40 bg-transparent text-danger shadow-xs hover:bg-danger-bg"
              onClick={() => navigate("policies")}
            >
              {LEAVE}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// approversOf are the roles that may hold a decision: the approver kind,
// and the console's own administrators.
function approversOf(roles: RoleRow[]): RoleRow[] {
  return roles.filter((r) => r.kind === "approver" || r.name === "straza-admin");
}
