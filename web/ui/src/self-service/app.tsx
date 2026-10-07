// The self-service page's shell: the
// console's header without the sidebar, the page head with the tab's one
// action, and three tabs in the address, Requests, Credentials and This
// browser. The page has two identities that never mix: the login session,
// shared per tab with the console, opens the credentials and the enrolment;
// the enrolled device, kept in IndexedDB, decides and needs no sign-in.
// Neither is a wall: a fresh browser lands on the page and reads what each
// tab can do for it.
import * as React from "react";
import { ExternalLinkIcon, MonitorIcon, SmartphoneIcon } from "lucide-react";
import { StrazaMark } from "@/components/straza-mark";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Toaster } from "@/components/ui/sonner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PageHead } from "@/components/page-head";
import { ScreenBoundary } from "@/components/screen-boundary";
import { SignIn } from "@/components/sign-in";
import { type ApiError, type SelfAnswer, readSelf } from "@/lib/api";
import { useSession } from "@/lib/use-session";
import { useTheme } from "@/lib/theme";
import { useTimeZone } from "@/lib/timezone";
import { bindDevice } from "./approver-api";
import { type Notice, SelfContext, type SelfState, type SessionInfo } from "./context";
import { teardown as pushTeardown } from "./push";
import { type SelfTab, navigate, useSelfRoute } from "./router";
import { signMessageB64 } from "./sign";
import { type Enrollment, type StorageOutlook, loadEnrollment, patchEnrollment, storageAvailable, storageOutlook, wipeEnrollment } from "./store";
import * as W from "./words";

const RequestsTab = React.lazy(() => import("./requests-tab").then((m) => ({ default: m.RequestsTab })));
const CredentialsTab = React.lazy(() => import("./credentials-tab").then((m) => ({ default: m.CredentialsTab })));
const BrowserTab = React.lazy(() => import("./browser-tab").then((m) => ({ default: m.BrowserTab })));

// BOOT_STORAGE_MS bounds the storage reads at boot: an IndexedDB open jammed
// by another tab must not strand the page.
const BOOT_STORAGE_MS = 3000;

function bounded<T>(p: Promise<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("it did not answer within 3 seconds")), BOOT_STORAGE_MS);
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });
}

type Counts = Partial<Record<SelfTab, number | null>>;

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
  const [signInOpen, setSignInOpen] = React.useState(false);
  const [sheets, setSheets] = React.useState({ enable: false, phone: false });
  const [counts, setCounts] = React.useState<Counts>({});

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
        setNotice({ tone: "danger", text: W.REVOKED_BY_ADMIN });
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
  // may enrol, computed by the same server code that gates the mint.
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

  // The doors the tabs call from effects keep one identity across renders:
  // a tab that lists a door among an effect's dependencies would otherwise
  // re-run it on every render of this shell, and a count it sets moves the
  // shell, which is a loop.
  const setCount = React.useCallback((t: SelfTab, n: number | null) => setCounts((c) => (c[t] === n ? c : { ...c, [t]: n })), []);
  const openSignIn = React.useCallback(() => setSignInOpen(true), []);
  const setSheet = React.useCallback((which: "enable" | "phone", open: boolean) => setSheets((s) => (s[which] === open ? s : { ...s, [which]: open })), []);

  const session: SessionInfo = state.kind === "signed-in"
    ? { kind: "signed-in", user: state.user, grants: state.grants, servers: state.servers }
    : state.kind === "booting" ? { kind: "booting" } : { kind: "signed-out" };

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
  };

  // The This browser tab's actions: Enable this browser is the primary while
  // this browser is not enrolled and the account may enrol one, and Add a
  // phone stands beside it for an account that may enrol a phone, the
  // console's own sheet with the person's own mint.
  const channels = self !== null && self !== "error" ? self.enroll_channels || [] : [];
  const showEnable = tab === "browser" && booted && !enrollment && session.kind === "signed-in" && channels.includes("browser");
  const showPhone = tab === "browser" && booted && session.kind === "signed-in" && channels.includes("mobile");
  const actions = showEnable || showPhone ? (
    <>
      {showPhone && (
        <Button variant={showEnable ? "outline" : "default"} onClick={() => setSheet("phone", true)} data-add-phone><SmartphoneIcon /> {W.ADD_PHONE}</Button>
      )}
      {showEnable && <Button onClick={() => setSheet("enable", true)} data-enable-browser><MonitorIcon /> {W.ENABLE_BROWSER}</Button>}
    </>
  ) : undefined;

  const onSignOut = async () => {
    await signOut();
    setNotice({ tone: "ok", text: enrollment ? W.SIGNED_OUT_ENROLLED : W.SIGNED_OUT });
  };

  return (
    <SelfContext.Provider value={value}>
      <TooltipProvider delayDuration={200}>
        <div className="flex min-h-screen flex-col bg-background text-foreground" data-self-service-app>
          <header className="flex h-12 items-center gap-3 border-b border-border bg-card px-6" data-self-header>
            <span className="flex items-center gap-2 font-semibold"><StrazaMark className="size-5 shrink-0" />{W.BRAND}</span>
            <span className="self-header-actions ml-auto flex items-center gap-3 text-[13px] text-text-2">
              {session.kind === "signed-in" && (session.grants || session.servers > 0) && (
                <Button variant="outline" size="sm" asChild><a href="/console/">{W.OPEN_CONSOLE} <ExternalLinkIcon /></a></Button>
              )}
              {session.kind === "signed-in" && <span data-signed-in={session.user}>{W.signedInAs(session.user)}</span>}
              {session.kind === "signed-in" && <Button variant="ghost" size="sm" onClick={() => void onSignOut()}>{W.SIGN_OUT}</Button>}
              {session.kind === "signed-out" && <Button variant="outline" size="sm" onClick={() => setSignInOpen(true)} data-sign-in>{W.SIGN_IN}</Button>}
              {session.kind === "booting" && <span className="text-muted-foreground">{W.RESUMING}</span>}
            </span>
          </header>
          <main className="min-h-0 flex-1">
            <PageHead label={W.PAGE_TITLE} description={W.PAGE_DESC} actions={actions} />
            {notice && (
              <div className={"mx-6 mt-4 rounded-md border border-border border-l-[3px] bg-card px-3 py-2 text-sm text-text-2 " + noticeEdge(notice.tone)} role="status" data-notice={notice.tone}>
                {notice.text}
              </div>
            )}
            <div className="flex flex-col gap-4 px-6 py-5">
              <Tabs value={tab} onValueChange={(v) => navigate(v as SelfTab, true)}>
                <div className="flex items-center border-b border-border">
                  <TabsList variant="line">
                    {W.TABS.map((t) => {
                      const n = counts[t.key];
                      return (
                        <TabsTrigger key={t.key} value={t.key} data-tab={t.key}>
                          {t.label} {n != null && <span className="font-mono text-xs text-muted-foreground" data-count={t.key}>{n}</span>}
                        </TabsTrigger>
                      );
                    })}
                  </TabsList>
                </div>
                <React.Suspense fallback={<p className="py-4 text-sm text-muted-foreground">{W.OPENING}</p>}>
                  <TabsContent value="requests"><ScreenBoundary key="requests"><RequestsTab /></ScreenBoundary></TabsContent>
                  <TabsContent value="credentials"><ScreenBoundary key="credentials"><CredentialsTab /></ScreenBoundary></TabsContent>
                  <TabsContent value="browser"><ScreenBoundary key="browser"><BrowserTab /></ScreenBoundary></TabsContent>
                </React.Suspense>
              </Tabs>
            </div>
          </main>
        </div>
        <Dialog open={signInOpen} onOpenChange={setSignInOpen}>
          <DialogContent className="sm:max-w-[460px]" data-sign-in-dialog>
            <DialogHeader>
              <DialogTitle>{W.SIGN_IN_TITLE}</DialogTitle>
              <DialogDescription>{W.SIGN_IN_BODY}</DialogDescription>
            </DialogHeader>
            <SignIn
              reason={{ kind: "none", detail: "" }}
              returnTo={W.PAGE_TITLE}
              adminOnly={false}
              embedded
              onAuthed={(resp) => { signedIn(resp); setSignInOpen(false); }}
            />
          </DialogContent>
        </Dialog>
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
