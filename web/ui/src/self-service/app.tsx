// The self-service page's shell: a header with the brand, the person's menu
// and the console door, then four tabs in the address, Home, Requests,
// Credentials and Devices, each opened by a head of its own. The page has
// two identities that never mix: the login session, shared per tab with the
// console, opens the credentials and the setup; the set-up device, kept in
// IndexedDB, decides and needs no sign-in. Neither is a wall: a fresh
// browser lands on Home and reads what each part can do for it.
import * as React from "react";
import { ChevronDownIcon, ExternalLinkIcon, MenuIcon, SmartphoneIcon, XIcon } from "lucide-react";
import { StrazaMark } from "@/components/straza-mark";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Toaster } from "@/components/ui/sonner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ScreenBoundary } from "@/components/screen-boundary";
import { type ApiError, type ApprovalRow, type SelfAnswer, readSelf, selfEnrollToken, selfServers } from "@/lib/api";
import { DOWN, beginSignIn, hasResume, takeSignInProblem } from "@/lib/session";
import { useSession } from "@/lib/use-session";
import { useTheme } from "@/lib/theme";
import { useTimeZone } from "@/lib/timezone";
import { bindDevice } from "./approver-api";
import { type Notice, SelfContext, type SelfState, type SessionInfo, standingOf } from "./context";
import { type CredentialRow, needing } from "./credential-rows";
import { teardown as pushTeardown } from "./push";
import { type SelfTab, navigate, useSelfRoute } from "./router";
import { signMessageB64 } from "./sign";
import { type Enrollment, type StorageOutlook, loadEnrollment, patchEnrollment, storageAvailable, storageOutlook, wipeEnrollment } from "./store";
import * as W from "./words";

const HomeTab = React.lazy(() => import("./home-tab").then((m) => ({ default: m.HomeTab })));
const RequestsTab = React.lazy(() => import("./requests-tab").then((m) => ({ default: m.RequestsTab })));
const CredentialsTab = React.lazy(() => import("./credentials-tab").then((m) => ({ default: m.CredentialsTab })));
const DevicesTab = React.lazy(() => import("./devices-tab").then((m) => ({ default: m.DevicesTab })));
const SetupDialog = React.lazy(() => import("./setup-dialog").then((m) => ({ default: m.SetupDialog })));
const EnrollSheet = React.lazy(() => import("@/components/approver-enroll-sheet").then((m) => ({ default: m.EnrollSheet })));

// BOOT_STORAGE_MS bounds the storage reads at boot: an IndexedDB open jammed
// by another tab must not strand the page.
const BOOT_STORAGE_MS = 3000;

function bounded<T>(p: Promise<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("it did not answer within 3 seconds")), BOOT_STORAGE_MS);
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });
}

// sayable is a sign-in sentence as this page shows it. DOWN, the session
// module's sentence for a sign-in that could not reach Straza, asks the
// reader to check that Straza runs, so the page shows its own in its place.
function sayable(text: string): string {
  return text === DOWN ? W.NO_ANSWER : text;
}

type Counts = Partial<Record<SelfTab, number | null>>;
type Sheets = SelfState["sheets"];

export function SelfServiceApp() {
  const { state, signedIn, signOut } = useSession({ adminOnly: false });
  const tab = useSelfRoute();
  const { resolved } = useTheme();
  useTimeZone();

  const [self, setSelf] = React.useState<SelfAnswer | null | "error">(null);
  const [enrollment, setEnrollmentState] = React.useState<Enrollment | null>(null);
  const [booted, setBooted] = React.useState(false);
  const [storage, setStorage] = React.useState<{ usable: boolean; outlook: StorageOutlook }>({ usable: true, outlook: "unknown" });
  const [notice, setNotice] = React.useState<Notice | null>(null);
  const [sheets, setSheets] = React.useState<Sheets>({ enable: false, phone: false });
  // drawn says a sheet was opened once, so its code loads on the first
  // press and stays mounted for its closing animation.
  const [drawn, setDrawn] = React.useState<Sheets>({ enable: false, phone: false });
  const [counts, setCounts] = React.useState<Counts>({});
  const [waiting, setWaiting] = React.useState<ApprovalRow[] | null | "error">(null);
  const [servers, setServers] = React.useState<CredentialRow[] | null | "error">(null);
  const [serverReads, setServerReads] = React.useState(0);

  React.useEffect(() => { document.title = W.DOC_TITLE; }, []);

  // bind puts a record into the approver lane with the two callbacks the
  // refresh and revoke doctrine needs: a refreshed token is patched into
  // the record, and a revoked device tears down its push route, wipes the
  // record and tells the person in red.
  const bind = React.useCallback((rec: Enrollment) => {
    bindDevice({
      id: rec.deviceId,
      token: rec.deviceToken,
      sign: (message) => signMessageB64(rec.keys.privateKey, message),
      onToken: async (token, expiresIn) => {
        rec.deviceToken = token;
        rec.tokenExpiresAt = Date.now() + expiresIn * 1000;
        await patchEnrollment({ deviceToken: token, tokenExpiresAt: rec.tokenExpiresAt });
      },
      onRevoked: async () => {
        await pushTeardown();
        await wipeEnrollment();
        bindDevice(null);
        setEnrollmentState(null);
        setNotice({ tone: "danger", text: W.REVOKED });
      },
    });
  }, []);

  const setEnrollment = React.useCallback((rec: Enrollment | null) => {
    if (rec) bind(rec);
    else bindDevice(null);
    setEnrollmentState(rec);
  }, [bind]);

  // Boot: the storage outlook and the stored enrolment, each bounded.
  React.useEffect(() => {
    let alive = true;
    (async () => {
      const usable = storageAvailable();
      let outlook: StorageOutlook = "unknown";
      let rec: Enrollment | null = null;
      let broken = "";
      if (usable) {
        outlook = await bounded(storageOutlook()).catch(() => "unknown" as StorageOutlook);
        try {
          rec = await bounded(loadEnrollment());
        } catch (e) {
          broken = e instanceof Error ? e.message : String(e);
        }
      }
      if (!alive) return;
      setStorage({ usable: usable && !broken, outlook });
      if (broken) setNotice({ tone: "danger", text: W.STORAGE_BROKEN(broken) });
      if (rec) setEnrollment(rec);
      setBooted(true);
    })();
    return () => { alive = false; };
  }, [setEnrollment]);

  // The /v1/self read follows the session: who this is and what the account
  // may set up, computed by the same server code that gates the mint.
  const refreshSelf = React.useCallback(async () => {
    try {
      setSelf(await readSelf());
    } catch (e) {
      if ((e as ApiError).status === 401) setSelf(null);
      else setSelf("error");
    }
  }, []);
  React.useEffect(() => {
    if (state.kind === "signed-in") void refreshSelf();
    else setSelf(null);
  }, [state.kind, refreshSelf]);

  // The credentials the Credentials count and Home's second card read: the
  // person's rows, then each sponsored agent's, one call after another as
  // the Credentials tab reads them. It runs once the person and their
  // agents are known, and again when a tab reports a change.
  const signedInNow = state.kind === "signed-in";
  const selfRead = self !== null;
  const sponsoredKey = self && self !== "error" ? (self.sponsored || []).join(",") : "";
  React.useEffect(() => {
    if (!signedInNow || !selfRead) {
      setServers(null);
      return undefined;
    }
    let alive = true;
    const names = sponsoredKey ? sponsoredKey.split(",") : [];
    void (async () => {
      try {
        const got: CredentialRow[] = ((await selfServers("")) || []).map((r) => ({ ...r, who: "" }));
        for (const u of names) got.push(...((await selfServers(u)) || []).map((r) => ({ ...r, who: u })));
        if (alive) setServers(got);
      } catch (e) {
        // A 401 has already ended the session inside the api module, so the
        // page redraws as signed out and says nothing here.
        if (alive && (e as ApiError).status !== 401) setServers("error");
      }
    })();
    return () => { alive = false; };
  }, [signedInNow, selfRead, sponsoredKey, serverReads]);
  const readServers = React.useCallback(() => setServerReads((n) => n + 1), []);
  const ownNeeding = Array.isArray(servers) ? needing(servers) : null;
  const ownNeedingRef = React.useRef<number | null>(null);
  ownNeedingRef.current = ownNeeding;

  // The doors the tabs call from effects keep one identity across renders:
  // a tab that lists a door among an effect's dependencies would otherwise
  // re-run it on every render of this shell, and a count it sets moves the
  // shell, which is a loop. A Credentials count that differs from the
  // shell's own says a credential changed on that tab, so the shell reads
  // again and Home follows.
  const setCount = React.useCallback((t: SelfTab, n: number | null) => {
    setCounts((c) => (c[t] === n ? c : { ...c, [t]: n }));
    const own = ownNeedingRef.current;
    if (t === "credentials" && n !== null && own !== null && n !== own) setServerReads((x) => x + 1);
  }, []);

  // The waiting requests belong to one lane, the device or the sign-in, so
  // a change of either drops them until the queue reads again.
  const lane = (enrollment ? enrollment.deviceId : "") + "|" + state.kind;
  React.useEffect(() => { setWaiting(null); }, [lane]);

  // Sign in sends this tab to the identity provider and back to the tab the
  // person is on. A failure to leave shows as the page's notice.
  const openSignIn = React.useCallback(() => {
    beginSignIn({ back: window.location.pathname }).catch((e: unknown) => {
      const text = (e as { unreachable?: boolean }).unreachable ? W.NO_ANSWER : e instanceof Error ? e.message : String(e);
      setNotice({ tone: "warn", text: sayable(text) });
    });
  }, []);
  // A sign-in that failed after the identity provider, or came back without
  // an answer, says why once on arrival.
  React.useEffect(() => {
    const say = () => {
      const problem = takeSignInProblem();
      if (problem) setNotice({ tone: "warn", text: sayable(problem) });
    };
    say();
    // A Back from the identity provider can restore this page from the
    // back-forward cache, which runs no effect, so the restore asks again.
    const onShow = (e: PageTransitionEvent) => { if (e.persisted) say(); };
    window.addEventListener("pageshow", onShow);
    return () => window.removeEventListener("pageshow", onShow);
  }, []);
  // opener is the element that had the focus when the setup dialog opened,
  // which takes the focus back when the dialog closes.
  const opener = React.useRef<HTMLElement | null>(null);
  const setSheet = React.useCallback((which: keyof Sheets, open: boolean) => {
    if (open && which === "enable") {
      const el = document.activeElement;
      opener.current = el instanceof HTMLElement && el !== document.body ? el : null;
    }
    if (open) setDrawn((s) => (s[which] ? s : { ...s, [which]: true }));
    setSheets((s) => (s[which] === open ? s : { ...s, [which]: open }));
  }, []);
  // A setup run that left to sign in opens again once the session it came
  // back with is live, and the dialog carries on.
  React.useEffect(() => {
    if (state.kind === "signed-in" && booted && hasResume()) setSheet("enable", true);
  }, [state.kind, booted, setSheet]);

  const session: SessionInfo = state.kind === "signed-in"
    ? { kind: "signed-in", user: state.user, grants: state.grants, servers: state.servers }
    : state.kind === "booting" ? { kind: "booting" } : { kind: "signed-out" };

  // Add a phone is the console's sheet over the person's own mint; the
  // phone then approves the requests routed to them, this page aside.
  const me = (session.kind === "signed-in" ? session.user : "") || (self && self !== "error" ? self.username : "");
  const phone = React.useMemo(() => ({
    username: me,
    mint: async () => {
      const a = await selfEnrollToken("mobile");
      return { ...a, servers: a.servers || [], user: { id: (a.user && a.user.id) || "", username: (a.user && a.user.username) || me } };
    },
  }), [me]);

  const value: SelfState = {
    session,
    self,
    refreshSelf,
    enrollment,
    setEnrollment,
    storage,
    openSignIn,
    signedIn,
    notice,
    setNotice,
    sheets,
    setSheet,
    setCount,
    waiting,
    setWaiting,
    servers,
    readServers,
  };

  // A tab's head takes focus when the person moves to another tab, so a
  // screen reader starts at the new tab's title. A tab opens on the press of
  // the pointer, and the browser then focuses the tab it pressed, so the
  // move waits until that press is over.
  const head = React.useRef<HTMLHeadingElement>(null);
  const shown = React.useRef(tab);
  React.useEffect(() => {
    if (shown.current === tab) return undefined;
    shown.current = tab;
    const t = setTimeout(() => head.current?.focus(), 0);
    return () => clearTimeout(t);
  }, [tab]);

  // toOpener gives the focus back to what opened the setup dialog, however
  // it closed. A setup that landed took the setup card's button away, and a
  // request dialog closed itself to open this one, so the tab's title takes
  // the focus when the opener is gone.
  const toOpener = React.useCallback((e: Event) => {
    e.preventDefault();
    const el = opener.current;
    opener.current = null;
    (el && el.isConnected ? el : head.current)?.focus();
  }, []);

  const standing = standingOf({ session, self });
  const action = tab === "devices" && standing.kind === "read" && standing.mobile
    ? <Button variant="outline" onClick={() => setSheet("phone", true)} data-add-phone><SmartphoneIcon /> {W.ADD_PHONE}</Button>
    : null;

  // countOf is the number a tab carries: the requests that are the reader's
  // to decide, and the credentials that wait for the person. The Credentials
  // tab's own count leads while it is open. A zero is not drawn.
  const countOf = (t: SelfTab): number | null => {
    if (t === "requests") return Array.isArray(waiting) ? waiting.length : null;
    if (t === "credentials") return counts.credentials ?? ownNeeding;
    return null;
  };

  const onSignOut = async () => {
    await signOut();
    setNotice({ tone: "ok", text: enrollment ? W.SIGNED_OUT_ENROLLED : W.SIGNED_OUT });
  };
  const door = session.kind === "signed-in" && (!!session.grants || session.servers > 0);
  const heading = W.TAB_HEAD[tab];

  return (
    <SelfContext.Provider value={value}>
      <TooltipProvider delayDuration={200}>
        <div className="flex min-h-screen flex-col bg-background text-foreground" data-self-service-app>
          <header className="flex h-12 items-center gap-3 border-b border-border bg-card px-6" data-self-header>
            <span className="flex min-w-0 items-center gap-2">
              <StrazaMark className="size-5 shrink-0" />
              <span className="font-semibold">{W.BRAND}</span>
              <span className="text-muted-foreground" data-page-title>{W.PAGE_TITLE}</span>
            </span>
            <span className="ml-auto flex min-w-0 items-center justify-end gap-3">
              <span className="hidden min-w-0 items-center gap-3 sm:flex" data-header-row>
                {door && <Button variant="outline" size="sm" asChild><a href="/console/">{W.OPEN_CONSOLE} <ExternalLinkIcon /></a></Button>}
                {session.kind === "signed-in" && (
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="sm" className="min-w-0 max-w-48" title={session.user} data-signed-in={session.user}><span className="truncate">{session.user}</span><ChevronDownIcon aria-hidden="true" /></Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onSelect={() => void onSignOut()}>{W.SIGN_OUT}</DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
                {session.kind === "signed-out" && <Button variant="outline" size="sm" onClick={openSignIn} data-sign-in>{W.SIGN_IN}</Button>}
                {session.kind === "booting" && <span className="text-caption text-muted-foreground">{W.RESUMING}</span>}
              </span>
              <span className="sm:hidden" data-header-menu>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon" aria-label={W.MENU}><MenuIcon /></Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {session.kind === "signed-in" && <DropdownMenuLabel className="[overflow-wrap:anywhere]">{session.user}</DropdownMenuLabel>}
                    {door && <DropdownMenuItem asChild><a href="/console/">{W.OPEN_CONSOLE} <ExternalLinkIcon /></a></DropdownMenuItem>}
                    {session.kind === "signed-in" && <DropdownMenuItem onSelect={() => void onSignOut()}>{W.SIGN_OUT}</DropdownMenuItem>}
                    {session.kind === "signed-out" && <DropdownMenuItem onSelect={openSignIn}>{W.SIGN_IN}</DropdownMenuItem>}
                    {session.kind === "booting" && <DropdownMenuLabel className="font-normal text-muted-foreground">{W.RESUMING}</DropdownMenuLabel>}
                  </DropdownMenuContent>
                </DropdownMenu>
              </span>
            </span>
          </header>
          <main className="min-h-0 flex-1">
            <div className="flex flex-col px-6 pb-8">
              {/* Arrow keys move along the tabs and Enter opens one, since the open tab's title takes focus. */}
              <Tabs value={tab} onValueChange={(v) => navigate(v as SelfTab, true)} activationMode="manual" className="gap-0">
                <div className="flex items-center border-b border-border">
                  <TabsList variant="line">
                    {W.TABS.map((t) => {
                      const n = countOf(t.key);
                      return (
                        <TabsTrigger key={t.key} value={t.key} data-tab={t.key}>
                          {t.label} {n ? <span className="font-mono text-caption leading-4 text-muted-foreground" data-count={t.key}>{n}</span> : null}
                        </TabsTrigger>
                      );
                    })}
                  </TabsList>
                </div>
                {notice && (
                  <div className={"mt-4 flex items-start gap-2 rounded-md border border-border border-l-[3px] bg-card py-1 pr-1 pl-3 text-dense text-text-2 " + noticeEdge(notice.tone)} role="status" data-notice={notice.tone}>
                    <p className="m-0 min-w-0 flex-1 py-1.5">{notice.text}</p>
                    <Button variant="ghost" size="icon-sm" aria-label={W.DISMISS} onClick={() => setNotice(null)}><XIcon /></Button>
                  </div>
                )}
                <div className="flex flex-wrap items-start gap-3 pt-5 pb-4" data-tab-head={tab}>
                  <div className="min-w-0 flex-1">
                    <h1 ref={head} tabIndex={-1} className="text-title leading-7 font-semibold text-foreground outline-none">{heading}</h1>
                  </div>
                  {action && <div className="flex flex-wrap items-center gap-2">{action}</div>}
                </div>
                <React.Suspense fallback={<p className="py-4 text-dense text-muted-foreground">{W.OPENING}</p>}>
                  <TabsContent value="home"><ScreenBoundary key="home"><HomeTab /></ScreenBoundary></TabsContent>
                  <TabsContent value="requests"><ScreenBoundary key="requests"><RequestsTab /></ScreenBoundary></TabsContent>
                  <TabsContent value="credentials"><ScreenBoundary key="credentials"><CredentialsTab /></ScreenBoundary></TabsContent>
                  <TabsContent value="devices"><ScreenBoundary key="devices"><DevicesTab /></ScreenBoundary></TabsContent>
                </React.Suspense>
              </Tabs>
            </div>
          </main>
        </div>
        <React.Suspense fallback={null}>
          {drawn.enable && <SetupDialog open={sheets.enable} onOpenChange={(o) => setSheet("enable", o)} onCloseAutoFocus={toOpener} />}
          {drawn.phone && <EnrollSheet open={sheets.phone} onOpenChange={(o) => setSheet("phone", o)} self={phone} phonePaste={W.PHONE_PASTE} />}
        </React.Suspense>
        <Toaster theme={resolved} closeButton />
      </TooltipProvider>
    </SelfContext.Provider>
  );
}

function noticeEdge(tone: Notice["tone"]): string {
  switch (tone) {
    case "ok": return "border-l-ok";
    case "warn": return "border-l-warn";
    case "danger": return "border-l-danger";
    default: return "border-l-unknown";
  }
}
