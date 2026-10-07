import * as React from "react";
import { FileXIcon, MoreHorizontalIcon, PlayIcon, PlusIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { OriginLine } from "@/components/origin-line";
import { PageHead } from "@/components/page-head";
import { PolicyDecisions } from "@/components/policy-decisions";
import { PolicyFacts, RecChip } from "@/components/policy-facts";
import { PolicyProof } from "@/components/policy-proof";
import { PolicyPublish } from "@/components/policy-publish";
import { PolicyRuleSheet, NEW_SHEET } from "@/components/policy-rule-sheet";
import { PolicyRules } from "@/components/policy-rules";
import { PolicyTest } from "@/components/policy-test";
import { PolicyUnpublished } from "@/components/policy-unpublished";
import { PolicyYaml } from "@/components/policy-yaml";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type EventSupport, type PolicyDoc, type RoleRow, activatePolicy, applyPolicy, deactivatePolicy, deletePolicy, eventSupport, getPolicy, listRoles, validatePolicy } from "@/lib/api";
import { type Doc, type Plain, addRule, docProblems, docText, openDoc, posturesOf, posturesOfRules, proposeRuleId, readSet, removeRule, splitRule } from "@/lib/policy-model";
import {
  ADD_RULE, CANCEL, DELETE, DELETE_BODY, DELETE_LIVE_TITLE, DRAFT, DRAFT_TITLE, EDITED, EDITED_TITLE, LEAVE, LEAVE_BODY, LEAVE_TITLE,
  LIVE, LIVE_TITLE, MENU, META, MISSING_BODY, MISSING_TITLE, MORE, NOT_PARSED_PAGE, OFF_HELP, OFF_KEEPS, ON_BODY, OPEN_POLICIES,
  READING_POLICY, SAVE, STAY, SUBJECT_POLICY, SUBJECT_ROLES, TAB, TEST_A_CALL, TOUCHED, TURN_OFF, TURN_ON,
  deleteTitle, deletedToast, metaRules, missingPolicy, movedOut, offBody, offTitle, offToast, onTitle, onToast, policyFileName, savedToast,
} from "@/lib/policy-words";
import { take } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { navigate, setLeaveGuard } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed, refused } from "@/lib/say";
import { agoWord } from "@/lib/settings-words";
import { absTime } from "@/lib/words";
import { downloadText } from "@/lib/utils";

// One policy's page. The page owns the working text: every row, fact
// sheet and keystroke edits the one stored document in place, so an edit
// keeps the comment lines, and nothing reaches the server until Save or
// Publish.

const route = routeByKey("policies");
const TABS = ["rules", "yaml", "decisions"] as const;

type Props = { name: string; tab?: string };

type State =
  | { kind: "loading" }
  | { kind: "missing" }
  | { kind: "error"; message: string }
  | { kind: "ready"; stored: PolicyDoc };

// Ask is the confirm on screen: turning the policy off or on, deleting a
// draft, or leaving with edits that are not published.
type Ask = { kind: "off" } | { kind: "on" } | { kind: "delete" } | { kind: "leave"; go: () => void } | null;

// Proof is what the wizard hands the page after a publish: the banner
// reads it once and watches for the first decision.
type Proof = { name: string; snapshot?: string; at: string };

export function PolicyPage({ name, tab }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [proof] = React.useState<Proof | null>(() => {
    const handed = take<Proof>("policies-proof");
    return handed && handed.name === name ? handed : null;
  });
  const [text, setText] = React.useState("");
  const [changes, setChanges] = React.useState<string[]>([]);
  // added are the rules this page wrote, which the table marks as new
  // until they are published.
  const [added, setAdded] = React.useState<string[]>([]);
  const [sheet, setSheet] = React.useState<string | null>(null);
  const [roles, setRoles] = React.useState<RoleRow[]>([]);
  const [rolesProblem, setRolesProblem] = React.useState<string | null>(null);
  const [events, setEvents] = React.useState<EventSupport | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [lastRead, setLastRead] = React.useState<Date | null>(null);
  const [refusal, setRefusal] = React.useState<{ subject: string; message: string } | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [ask, setAsk] = React.useState<Ask>(null);
  const [testOpen, setTestOpen] = React.useState(false);
  const [publishing, setPublishing] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const stored = await getPolicy(name);
      setState({ kind: "ready", stored });
      setText(stored.yaml || "");
      setChanges([]);
      setAdded([]);
      setProblem(null);
      setLastRead(new Date());
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      if (err.status === 404) { setState({ kind: "missing" }); return; }
      const message = readFailed(SUBJECT_POLICY, err);
      setState((s) => (s.kind === "ready" ? s : { kind: "error", message }));
      setProblem(message);
    }
  }, [name]);

  React.useEffect(() => { void load(); }, [load]);

  // The roles feed the Applies to sheet and its holder counts; the event
  // matrix feeds the rule editor, which says nothing while it is null.
  React.useEffect(() => {
    listRoles().then(setRoles, (e) => {
      const err = e as ApiError;
      if (err.status !== 401) setRolesProblem(readFailed(SUBJECT_ROLES, err));
    });
    eventSupport().then(setEvents, () => setEvents(null));
  }, []);

  const current = TABS.includes((tab || "") as typeof TABS[number]) ? (tab as string) : TABS[0];
  React.useEffect(() => {
    if (!tab) navigate("policies", [name, TABS[0]], true);
  }, [name, tab]);

  // While edits sit on the page, every departure asks first: the router's
  // guard covers the sidebar, the palette and every door on the page, and
  // a tab change under the same policy passes. A tab close is the one
  // departure the page cannot ask about in its own words, so it leans on
  // the browser's.
  React.useEffect(() => {
    if (changes.length === 0) { setLeaveGuard(null); return; }
    setLeaveGuard((next, go) => {
      if (next.kind === "route" && next.key === "policies" && next.rest[0] === name) { go(); return; }
      setAsk({ kind: "leave", go });
    });
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => {
      setLeaveGuard(null);
      window.removeEventListener("beforeunload", warn);
    };
  }, [changes.length, name]);

  const parsed = React.useMemo(() => {
    const doc = openDoc(text);
    const problems = docProblems(doc);
    return { doc: problems.length ? null : doc, problems };
  }, [text]);
  const view = React.useMemo(() => (parsed.doc ? readSet(parsed.doc) : null), [parsed.doc]);

  const edit = React.useCallback((touched: string, fn: (doc: Doc) => void) => {
    const doc = openDoc(text);
    if (docProblems(doc).length) return;
    fn(doc);
    setText(docText(doc));
    setChanges((c) => (c.includes(touched) ? c : [...c, touched]));
  }, [text]);

  const replaceText = React.useCallback((next: string) => {
    setText(next);
    setChanges((c) => (c.includes(TOUCHED.text) ? c : [...c, TOUCHED.text]));
  }, []);

  if (state.kind === "loading") return <PageHead label={route.label} title={name} description={READING_POLICY} />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label={route.label} title={name} description={READING_POLICY} />
        <div className="flex flex-col gap-3 px-6 py-5"><FetchError subject={SUBJECT_POLICY} detail={state.message} /></div>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label={route.label} title={MISSING_TITLE} description={MISSING_BODY} />
        <EmptyState icon={FileXIcon} title={MISSING_TITLE} action={<Button variant="outline" onClick={() => navigate("policies")}>{OPEN_POLICIES}</Button>}>
          {missingPolicy(name)}
        </EmptyState>
      </>
    );
  }

  const { stored } = state;
  const live = stored.status === "active";
  const summary = stored.summary;
  const rules = view ? view.rules : [];
  const ruleCount = view ? rules.length : summary?.rules || 0;
  const appliesTo = view ? view.roles : summary?.matchRoles || [];
  const postures = view ? posturesOfRules(rules) : posturesOf(summary?.postures);
  // The head counts the stored policy, never the page: the number beside
  // the name says what is stored, and the bar says what the edits change.
  const livePostures = posturesOf(summary?.postures);
  const capture = view ? view.capture : (summary?.capture === "redact" ? "redact" : summary?.capture ? "verbatim" : null);
  const description = (view ? view.description : summary?.description) || "";

  const discard = () => {
    setText(stored.yaml || "");
    setChanges([]);
    setAdded([]);
    setSheet(null);
    setRefusal(null);
  };

  // Save draft stores the text through the policy route, which keeps it as
  // the set's saved edit when the set runs (a draft the origin line names)
  // and as its stored text when it is off, so a live policy keeps running
  // its published version until the next publish.
  const save = async () => {
    setRefusal(null);
    setBusy(true);
    try {
      await validatePolicy(text);
      await applyPolicy(text);
      notify.ok(savedToast(name, live));
      await load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: SAVE, message: refused(err) });
    } finally {
      setBusy(false);
    }
  };

  const turnOff = async () => {
    setRefusal(null);
    try {
      await deactivatePolicy(name);
      notify.ok(offToast(name));
      setAsk(null);
      await load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: TURN_OFF, message: refused(err) });
    }
  };

  const turnOn = async () => {
    setRefusal(null);
    try {
      await activatePolicy(name);
      notify.ok(onToast(name));
      setAsk(null);
      await load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: TURN_ON, message: refused(err) });
    }
  };

  const remove = async () => {
    setRefusal(null);
    try {
      await deletePolicy(name);
      notify.ok(deletedToast(name));
      navigate("policies");
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: DELETE, message: refused(err) });
    }
  };

  // Add rule opens the sheet with nothing chosen, so the first answer is
  // where the call goes rather than a denial the page wrote by itself.
  const openNew = () => {
    if (!parsed.doc) return;
    setSheet(NEW_SHEET);
    if (current !== "rules") navigate("policies", [name, "rules"], true);
  };

  const addOne = (id: string, rule: Plain) => {
    edit(id, (doc) => addRule(doc, rule));
    setAdded((a) => (a.includes(id) ? a : [...a, id]));
    setSheet(null);
  };

  // A call moved out of a rule becomes a rule of its own at the end of the
  // document, marked new, and the sheet follows it so what happens to it
  // can be changed at once.
  const split = (id: string, callName: string) => {
    const rule = rules.find((r) => r.id === id);
    if (!rule) return;
    const posture = rule.posture === "deny" ? "deny" : rule.posture === "allow" ? "allow" : rule.posture === "ticket" ? "ticket" : "hold";
    const newId = proposeRuleId(posture, rule.lane, rule.app, [callName], rules.map((r) => r.id));
    edit(id, (doc) => splitRule(doc, id, callName, newId));
    setChanges((c) => (c.includes(newId) ? c : [...c, newId]));
    setAdded((a) => (a.includes(newId) ? a : [...a, newId]));
    setSheet(newId);
    notify.ok(movedOut(callName, newId));
  };

  const badges = (
    <>
      <WordBadge word={live ? LIVE : DRAFT} tone={live ? "ok" : "plain"} title={live ? LIVE_TITLE : DRAFT_TITLE} attr="data-policy-status" />
      {stored.drift && <WordBadge word={EDITED} tone="warn" title={EDITED_TITLE} attr="data-drift" />}
      <RecChip capture={capture} />
    </>
  );

  const meta = (
    <span className="flex basis-full flex-wrap items-center gap-x-3 gap-y-1 text-[13px] text-muted-foreground" data-meta>
      <span>{metaRules(livePostures, summary?.rules || 0)}</span>
      <span aria-hidden="true">·</span>
      <span title={absTime(stored.updated_at)}>{META.updated + " " + agoWord(stored.updated_at)}</span>
    </span>
  );

  const actions = (
    <>
      <Button variant="outline" onClick={() => setTestOpen(true)}><PlayIcon /> {TEST_A_CALL}</Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" aria-label={MORE}><MoreHorizontalIcon /></Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => downloadText(policyFileName(name), text, "application/yaml")}>{MENU.export}</DropdownMenuItem>
          <DropdownMenuSeparator />
          {live
            ? <DropdownMenuItem onSelect={() => { setRefusal(null); setAsk({ kind: "off" }); }}>{MENU.off}</DropdownMenuItem>
            : <DropdownMenuItem onSelect={() => { setRefusal(null); setAsk({ kind: "on" }); }}>{MENU.on}</DropdownMenuItem>}
          {live
            ? <DropdownMenuItem className="text-danger" aria-disabled="true" title={DELETE_LIVE_TITLE} onSelect={(e) => e.preventDefault()}>{MENU.delete}</DropdownMenuItem>
            : <DropdownMenuItem className="text-danger" onSelect={() => { setRefusal(null); setAsk({ kind: "delete" }); }}>{MENU.delete}</DropdownMenuItem>}
        </DropdownMenuContent>
      </DropdownMenu>
      <Button
        variant={changes.length ? "outline" : "default"}
        aria-disabled={parsed.doc ? undefined : true}
        title={parsed.problems[0] || undefined}
        onClick={openNew}
      >
        <PlusIcon /> {ADD_RULE}
      </Button>
    </>
  );

  return (
    <>
      <div className="contents [&_h1]:font-mono">
        <PageHead label={route.label} title={name} titleExtra={badges} description={description} descriptionExtra={meta} actions={actions} />
      </div>

      <div className="flex flex-col gap-4 px-6 py-5">
        {problem && <FetchError subject={SUBJECT_POLICY} detail={problem} lastRead={lastRead} />}
        {rolesProblem && <FetchError subject={SUBJECT_ROLES} detail={rolesProblem} lastRead={lastRead} />}
        <OriginLine object={"PolicySet/" + name} name={name} />

        <Tabs value={current} onValueChange={(v) => navigate("policies", [name, v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              <TabsTrigger value="rules">{TAB.rules} <span className="font-mono text-xs text-muted-foreground">{ruleCount}</span></TabsTrigger>
              <TabsTrigger value="yaml">{TAB.yaml}</TabsTrigger>
              <TabsTrigger value="decisions">{TAB.decisions}</TabsTrigger>
            </TabsList>
          </div>

          {changes.length > 0 && (
            <div className="sticky top-0 z-10">
              <PolicyUnpublished
                changes={changes}
                blocked={parsed.problems[0] || null}
                busy={busy}
                pagePostures={postures}
                livePostures={livePostures}
                stale={!!stored.drift}
                onDiscard={discard}
                onShowChange={() => navigate("policies", [name, "yaml"], true)}
                onSave={() => void save()}
                onPublish={() => setPublishing(true)}
              />
            </div>
          )}
          {refusal?.subject === SAVE && <RefusedError subject={SAVE} message={refusal.message} />}

          <TabsContent value="rules" className="flex flex-col gap-3">
            {proof && <PolicyProof name={proof.name} snapshot={proof.snapshot} at={proof.at} />}
            {view ? (
              <>
                <PolicyFacts view={view} roles={roles} onEdit={edit} />
                <PolicyRules rules={rules} changed={changes} added={added} openId={sheet} onOpen={setSheet} />
              </>
            ) : (
              <div className="flex flex-col gap-1.5" data-not-parsed>
                <p className="max-w-[75ch] text-sm leading-relaxed text-text-2">{NOT_PARSED_PAGE}</p>
                {parsed.problems.map((p) => <p key={p} className="font-mono text-[13px] leading-relaxed text-danger">{p}</p>)}
              </div>
            )}
          </TabsContent>

          <TabsContent value="yaml">
            <PolicyYaml text={text} stored={stored.yaml || ""} onChange={replaceText} />
          </TabsContent>

          <TabsContent value="decisions">
            <PolicyDecisions name={name} onOpenAudit={() => navigate("audit")} />
          </TabsContent>
        </Tabs>
      </div>

      {parsed.doc && (
        <PolicyRuleSheet
          open={sheet}
          onOpenChange={(o) => { if (!o) setSheet(null); }}
          doc={parsed.doc}
          rules={rules}
          events={events}
          roles={roles}
          onEdit={edit}
          onRemove={(id) => { edit(id, (doc) => removeRule(doc, id)); setSheet(null); }}
          onSplit={split}
          onAdd={addOne}
        />
      )}

      <PolicyPublish
        open={publishing}
        onOpenChange={setPublishing}
        name={name}
        text={text}
        baseText={stored.yaml || ""}
        wasLive={live}
        turnOn={!live}
        roles={appliesTo}
        onDone={() => { setPublishing(false); void load(); }}
      />

      <PolicyTest open={testOpen} onOpenChange={setTestOpen} draft={changes.length ? { name, yaml: text } : null} />

      <AlertDialog open={ask?.kind === "off"} onOpenChange={(o) => { if (!o) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{offTitle(name)}</AlertDialogTitle>
            <AlertDialogDescription>{offBody(postures, appliesTo, capture !== null)}</AlertDialogDescription>
          </AlertDialogHeader>
          <p className="flex items-center gap-1.5 text-sm leading-relaxed text-text-2">
            {OFF_KEEPS}
            <HelpTip label={TURN_OFF} text={OFF_HELP} />
          </p>
          {refusal?.subject === TURN_OFF && <RefusedError subject={TURN_OFF} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className="border border-danger/40 bg-transparent text-danger shadow-xs hover:bg-danger-bg"
              onClick={(e) => { e.preventDefault(); void turnOff(); }}
            >
              {TURN_OFF}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={ask?.kind === "on"} onOpenChange={(o) => { if (!o) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{onTitle(name)}</AlertDialogTitle>
            <AlertDialogDescription>{ON_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal?.subject === TURN_ON && <RefusedError subject={TURN_ON} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction onClick={(e) => { e.preventDefault(); void turnOn(); }}>{TURN_ON}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={ask?.kind === "delete"} onOpenChange={(o) => { if (!o) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{deleteTitle(name)}</AlertDialogTitle>
            <AlertDialogDescription>{DELETE_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal?.subject === DELETE && <RefusedError subject={DELETE} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={(e) => { e.preventDefault(); void remove(); }}>{DELETE}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={ask?.kind === "leave"} onOpenChange={(o) => { if (!o) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{LEAVE_TITLE}</AlertDialogTitle>
            <AlertDialogDescription>{LEAVE_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{STAY}</AlertDialogCancel>
            <AlertDialogAction
              className="border border-danger/40 bg-transparent text-danger shadow-xs hover:bg-danger-bg"
              onClick={() => { const go = ask?.kind === "leave" ? ask.go : null; setAsk(null); if (go) go(); }}
            >
              {LEAVE}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
