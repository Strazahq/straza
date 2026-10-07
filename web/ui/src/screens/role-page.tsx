import * as React from "react";
import { DownloadIcon, PencilIcon, ShieldOffIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { OriginLine } from "@/components/origin-line";
import { HelpTip } from "@/components/help-tip";
import { PageHead } from "@/components/page-head";
import { RoleAccess } from "@/components/role-access";
import { RoleAdministers } from "@/components/role-administers";
import { RoleAreas } from "@/components/role-areas";
import { RoleComposes } from "@/components/role-composes";
import { RoleHolders } from "@/components/role-holders";
import { RoleKindBadge } from "@/components/role-kind";
import { RolePacks } from "@/components/role-packs";
import { RolePolicies } from "@/components/role-policies";
import { type Built, RoleSaves, liveExport } from "@/components/role-saves";
import { type ApiError, type AppRow, type BindingRow, type ImplicationRow, type PackRow, type PoliciesAnswer, type PolicySetRow, type RoleRow, type ToolRow, deleteRole, exportRole, listApps, listBindings, listImplications, listPacks, listPolicies, listRoles, listTools, query } from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { AREAS, CANCEL, CHANGE_DESCRIPTION, DELETE_HELP, DELETE_ROLE, DESCRIPTION_HINT, DESCRIPTION_LABEL, EDIT_ACCESS, EXPORT_FAILED, EXPORT_YAML, KIND_HELP, MISSING_DESCRIPTION, MISSING_TITLE, NOTHING_CHANGED, NO_DESCRIPTION, OPEN_ROLES, PRIMARY, PRODUCT_ROLE, READING_ROLE, RELOAD, SUBJECT_COMPOSES, SUBJECT_POLICIES, SUBJECT_ROLE, TAB, deleteBody, deleteTitle, deletedToast, isProductRole, kindOf, list, mintedRole, missingRole, roleFileName } from "@/lib/role-words";
import { readFailed, refused } from "@/lib/say";
import { ownedChip, ownedChipTitle } from "@/lib/server-roles-words";
import { adminAreas } from "@/lib/session";
import { roleItem } from "@/lib/role-draft";
import { downloadText } from "@/lib/utils";

// One role's page: the head with the kind's own primary, and the tabs the
// kind has.
// The page owns every count in the tab strip, so it reads what each tab
// counts and hands the rows to the tab that renders them.

const route = routeByKey("roles");

type Props = { id: string; tab?: string };

// Problem is one side read that failed: the subject names it, the detail
// says what to do next.
type Problem = { subject: string; detail: string };

type Data = {
  role: RoleRow;
  roles: RoleRow[];
  bindings: BindingRow[];
  tools: ToolRow[];
  apps: AppRow[];
  implications: ImplicationRow[];
  sets: PolicySetRow[];
  packs: PackRow[];
  problems: Problem[];
  composesProblem: string | null;
  policiesProblem: string | null;
};

type State =
  | { kind: "loading" }
  | { kind: "missing" }
  | { kind: "error"; message: string }
  | { kind: "ready"; data: Data; problem: string | null; lastRead: Date };

// why words a failed side read, or "" for one that landed and for a 401,
// which the session module answers on its own.
function why(subject: string, r: PromiseSettledResult<unknown>): string {
  if (r.status !== "rejected") return "";
  const err = r.reason as ApiError;
  return err.status === 401 ? "" : readFailed(subject, err);
}

const value = <T,>(r: PromiseSettledResult<T>, fallback: T): T => (r.status === "fulfilled" ? r.value : fallback);

export function RolePage({ id, tab }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [edit, setEdit] = React.useState<string | null>(null);
  // same is the description dialog's Save pressed on the live text.
  const [same, setSame] = React.useState(false);
  const [ask, setAsk] = React.useState(false);
  const [refusal, setRefusal] = React.useState<{ subject: string; message: string } | null>(null);
  const [sheetOpen, setSheetOpen] = React.useState(false);
  const [composeOpen, setComposeOpen] = React.useState(false);
  const [grantOpen, setGrantOpen] = React.useState(false);

  const load = React.useCallback(async () => {
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    let roles: RoleRow[];
    try {
      roles = await listRoles();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const message = readFailed(SUBJECT_ROLE, err);
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      return;
    }
    const role = roles.find((r) => r.id === id);
    if (!role) { setState({ kind: "missing" }); return; }
    // An approver role reads its pools off decider_in, so no policy list is
    // asked for; every other kind counts the sets that name it.
    const approver = kindOf(role) === "approver";
    const [bindingsR, toolsR, appsR, impR, setsR, packsR] = await Promise.allSettled([
      listBindings(),
      listTools(),
      listApps(),
      listImplications(id),
      approver ? Promise.resolve({ items: [] } as PoliciesAnswer) : listPolicies(query({ role: role.name, limit: 0 })),
      listPacks(),
    ]);
    const problems = [
      { subject: TAB.access, detail: why(TAB.access, bindingsR) || why(TAB.access, toolsR) || why(TAB.access, appsR) },
      { subject: TAB.packs, detail: why(TAB.packs, packsR) },
    ].filter((p) => p.detail);
    setState({
      kind: "ready",
      lastRead: new Date(),
      problem: null,
      data: {
        role,
        roles,
        bindings: value(bindingsR, [] as BindingRow[]),
        tools: value(toolsR, [] as ToolRow[]),
        apps: value(appsR, [] as AppRow[]),
        implications: value(impR, [] as ImplicationRow[]),
        sets: value(setsR, { items: [] } as PoliciesAnswer).items || [],
        packs: value(packsR, [] as PackRow[]),
        problems,
        composesProblem: why(SUBJECT_COMPOSES, impR) || null,
        policiesProblem: why(SUBJECT_POLICIES, setsR) || null,
      },
    });
  }, [id]);

  React.useEffect(() => { void load(); }, [load]);

  if (state.kind === "loading") return <PageHead label={route.label} title={route.label} description={READING_ROLE} />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label={route.label} title={route.label} description={READING_ROLE} />
        <div className="flex flex-col items-start gap-3 px-6 py-5">
          <FetchError subject={SUBJECT_ROLE} detail={state.message} />
          <Button variant="outline" size="sm" onClick={() => void load()}>{RELOAD}</Button>
        </div>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label={route.label} title={MISSING_TITLE} description={MISSING_DESCRIPTION} />
        <EmptyState icon={ShieldOffIcon} title={MISSING_TITLE} action={<Button variant="outline" onClick={() => navigate("roles")}>{OPEN_ROLES}</Button>}>
          {missingRole(id)}
        </EmptyState>
      </>
    );
  }

  const { role, roles, bindings, tools, apps, implications, sets, packs, problems, composesProblem, policiesProblem } = state.data;
  const kind = kindOf(role);
  const product = isProductRole(role.name);
  const mine = bindings.filter((b) => b.role === role.name);
  // An application role reaches one server, so its primary edits that row,
  // or the access of the server that owns it. A role that belongs to no
  // server and has no row has no server to edit access to, so it has none.
  const primaryWord = kind === "application" ? (role.server || mine.length > 0 ? EDIT_ACCESS : "") : PRIMARY[kind];
  // A session with the apps grant is the global admin of every server, the
  // standing the server page reads the same way.
  const standing = adminAreas();
  const globalAdmin = standing === null || !!standing.apps;
  // The servers this role administers are the servers that name it, so a
  // role Straza minted with a server is known here without reading its name.
  const administers = apps.filter((a) => a.admin_role_id === role.id);
  const areas = role.areas || [];
  const bound = packs.filter((p) => (p.bindings || []).some((b) => b.role_id === role.id));
  const policyCount = kind === "approver" ? (role.decider_in || []).length : sets.length;

  const tabs: [string, string, number][] = [];
  if (kind === "application") tabs.push(["access", TAB.access, mine.length]);
  if (kind === "business") tabs.push(["composes", TAB.composes, implications.length]);
  if (kind === "straza") tabs.push(["areas", TAB.areas, areas.includes("full") ? AREAS.length : areas.length]);
  if (administers.length > 0) tabs.push(["administers", TAB.administers, administers.length]);
  tabs.push(["holders", TAB.holders, role.holder_count || 0]);
  tabs.push(["policies", TAB.policies, policyCount]);
  if (packs.length > 0 && (kind === "application" || kind === "business")) tabs.push(["packs", TAB.packs, bound.length]);
  const current = tabs.some(([k]) => k === tab) ? (tab as string) : tabs[0][0];

  // The kind's primary opens its own tab's one write, so a click from
  // another tab moves there first.
  const openPrimary = () => {
    const wanted = kind === "application" ? "access" : kind === "business" ? "composes" : "holders";
    if (current !== wanted) navigate("roles", [id, wanted], true);
    if (kind === "application") setSheetOpen(true);
    else if (kind === "business") setComposeOpen(true);
    else setGrantOpen(true);
  };

  const exportYAML = async () => {
    try {
      downloadText(roleFileName(role.name), await exportRole(role.id), "application/yaml");
    } catch (e) {
      notify.failed(readFailed(EXPORT_FAILED, e as ApiError));
    }
  };

  // describe builds the description's Role put: the role's live export,
  // read now, with the one field changed.
  const describe = async (): Promise<Built> => {
    if ((edit || "") === (role.description || "")) { setSame(true); return null; }
    const text = await liveExport(role);
    if (typeof text !== "string") return text;
    return [roleItem(text, (s) => { s.description = edit || ""; })];
  };

  const remove = async () => {
    setRefusal(null);
    try {
      const answer = await deleteRole(role.id);
      notify.ok(deletedToast(role.name, answer?.sets_off));
      navigate("roles");
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: DELETE_ROLE, message: refused(err) });
    }
  };

  // The help behind the kind badge says why a role cannot be deleted here,
  // for a product role and for a role minted with a server.
  const kindHelp = product
    ? KIND_HELP[kind] + " " + PRODUCT_ROLE
    : administers.length > 0
      ? KIND_HELP[kind] + " " + mintedRole(list(administers.map((a) => a.name)))
      : KIND_HELP[kind];

  // The server that owns the role, when the servers list was read. Its
  // words open that server's Roles tab, where the role's tools are edited.
  const ownerApp = role.server ? apps.find((a) => a.name === role.server) : undefined;
  const owner = role.server ? (
    ownerApp ? (
      <Button
        variant="link"
        size="sm"
        className="h-auto p-0 text-[13px]"
        title={ownedChipTitle(role.server)}
        data-owned-by={role.server}
        onClick={() => navigate("servers", [ownerApp.id, "roles"])}
      >
        {ownedChip(role.server)}
      </Button>
    ) : (
      <span className="text-[13px] text-muted-foreground" title={ownedChipTitle(role.server)} data-owned-by={role.server}>{ownedChip(role.server)}</span>
    )
  ) : null;

  // A minted role goes with its server: removing the server removes the
  // role, so the page offers no delete of its own.
  const actions = (
    <>
      {!product && administers.length === 0 && (
        <Button variant="outline" className="border-danger text-danger hover:bg-danger-bg" onClick={() => { setRefusal(null); setAsk(true); }}>{DELETE_ROLE}</Button>
      )}
      <Button variant="outline" onClick={() => void exportYAML()}><DownloadIcon /> {EXPORT_YAML}</Button>
      {primaryWord && <Button onClick={openPrimary}>{primaryWord}</Button>}
    </>
  );

  return (
    <>
      <div className="contents [&_h1]:font-mono">
        <PageHead
          label={route.label}
          title={role.name}
          titleExtra={<><RoleKindBadge role={role} administers={administers.map((a) => a.name)} /><HelpTip label={kind} text={kindHelp} />{owner}</>}
          description={role.description || NO_DESCRIPTION}
          descriptionExtra={<Button variant="ghost" size="icon-xs" aria-label={CHANGE_DESCRIPTION} onClick={() => { setSame(false); setEdit(role.description || ""); }}><PencilIcon /></Button>}
          actions={actions}
        />
      </div>
      <div className="flex flex-col gap-4 px-6 py-5">
        {state.problem && <FetchError subject={SUBJECT_ROLE} detail={state.problem} lastRead={state.lastRead} />}
        {problems.map((p) => <FetchError key={p.subject} subject={p.subject} detail={p.detail} lastRead={state.lastRead} />)}
        <OriginLine object={"Role/" + role.name} name={role.name} />

        <Tabs value={current} onValueChange={(v) => navigate("roles", [id, v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              {tabs.map(([key, label, n]) => (
                <TabsTrigger key={key} value={key}>{label} <span className="font-mono text-xs text-muted-foreground">{n}</span></TabsTrigger>
              ))}
            </TabsList>
          </div>
          <TabsContent value="access">
            <RoleAccess role={role} bindings={bindings} apps={apps} tools={tools} sheetOpen={sheetOpen} onSheetOpenChange={setSheetOpen} onChanged={() => void load()} globalAdmin={globalAdmin} />
          </TabsContent>
          <TabsContent value="composes">
            <RoleComposes role={role} roles={roles} implications={implications} bindings={bindings} composeOpen={composeOpen} onComposeOpenChange={setComposeOpen} onChanged={() => void load()} problem={composesProblem} lastRead={state.lastRead} />
          </TabsContent>
          <TabsContent value="areas"><RoleAreas role={role} /></TabsContent>
          <TabsContent value="administers"><RoleAdministers servers={administers} /></TabsContent>
          <TabsContent value="holders">
            <RoleHolders role={role} roles={roles} grantOpen={grantOpen} onGrantOpenChange={setGrantOpen} onChanged={() => void load()} />
          </TabsContent>
          <TabsContent value="policies">
            <RolePolicies role={role} sets={sets} problem={policiesProblem} lastRead={state.lastRead} />
          </TabsContent>
          <TabsContent value="packs">
            <RolePacks role={role} packs={packs} onChanged={() => void load()} />
          </TabsContent>
        </Tabs>
      </div>

      <Dialog open={edit !== null} onOpenChange={(o) => { if (!o) setEdit(null); }}>
        <DialogContent className="sm:max-w-[560px]">
          <DialogHeader>
            <DialogTitle>{CHANGE_DESCRIPTION}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="role-description">{DESCRIPTION_LABEL}</Label>
            <Textarea id="role-description" value={edit || ""} onChange={(e) => { setSame(false); setEdit(e.target.value); }} rows={3} />
            <p className="text-[13px] text-muted-foreground">{DESCRIPTION_HINT}</p>
            {same && <p role="status" className="m-0 text-[13px] text-danger" data-save-note>{NOTHING_CHANGED}</p>}
          </div>
          <RoleSaves role={role.name} build={describe} onPublished={() => { setEdit(null); void load(); }} onCancel={() => setEdit(null)} />
        </DialogContent>
      </Dialog>

      <AlertDialog open={ask} onOpenChange={(o) => { if (!o) { setAsk(false); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{deleteTitle(role.name)}</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteBody(role.holder_count || 0, mine.length)}
              <HelpTip label={DELETE_ROLE} text={DELETE_HELP} className="ml-1" />
            </AlertDialogDescription>
          </AlertDialogHeader>
          {refusal?.subject === DELETE_ROLE && <RefusedError subject={DELETE_ROLE} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={(e) => { e.preventDefault(); void remove(); }}>{DELETE_ROLE}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
