import * as React from "react";
import { CopyIcon, Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError, RefusedError } from "@/components/error-state";
import { ToneBadge } from "@/components/overview-parts";
import { Section } from "@/components/sheet-parts";
import { type ApiError, type EnrollTokenAnswer, readSelf, selfEnrollToken } from "@/lib/api";
import {
  ADD_PHONE,
  ADD_PHONE_SUB,
  CANCEL,
  CLOSE,
  CODE_SPENT,
  CODE_TITLE,
  COPIED,
  COPY,
  CREATE_ANOTHER,
  CREATE_QR,
  ENROLL_TEXT_TITLE,
  FOR_OTHERS_LINE,
  FOR_OTHERS_TITLE,
  MINT_VERB,
  NO_ENROLL_ROLE,
  QR_LABEL,
  QR_TOO_BIG,
  SCAN_LINE,
  SCAN_TITLE,
  SERVERS_NONE,
  SERVERS_TITLE,
  SUBJECT_SELF,
  TRUST_TITLE,
  codeLeft,
  selfPhoneLine,
  trustPosture,
} from "@/lib/approval-words";
import { copyText } from "@/lib/clipboard";
import { type QrDrawing, qrDrawing } from "@/lib/qr";
import { readFailed, refused } from "@/lib/say";

// EnrollSheet is Add a phone: mint a one-time code for the signed-in
// person's own phone, and show the QR the Straza approver app scans beside
// the code itself, the servers the phone will try and how it will trust
// them. The code lives in this component's state alone and goes when the
// sheet closes; nothing writes it anywhere.

const HINT = "text-[13px] leading-snug text-muted-foreground";
const LINE = "text-[13px] leading-snug text-text-2";

// SelfMint is the person's own lane: the person is the signed-in one and
// the mint is their own, and everything after the mint is the console's.
export type SelfMint = { username: string; mint: () => Promise<EnrollTokenAnswer> };

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onMinted says a code was minted. The tab reads the list again, so a
  // device enrolled since the last poll is on screen while the operator
  // watches for the new one.
  onMinted?: () => void;
  // self hands in the self-service page's mint. The console names none, so
  // the sheet reads who is signed in and mints that person's own phone.
  self?: SelfMint;
};

// Draw is the QR's own state: the encoder loads on first use, and a text no
// QR code can carry is said in words and handed over as text instead.
type Draw = { kind: "drawing" } | { kind: "ready"; qr: QrDrawing } | { kind: "toobig"; text: string };

// Who is the console's read of GET /v1/self: the signed-in name, and
// whether the account may enroll a phone, from the same server code that
// gates the mint.
type Who = { kind: "reading" } | { kind: "ready"; username: string; mobile: boolean } | { kind: "error"; message: string };

export function EnrollSheet({ open, onOpenChange, onMinted, self }: Props) {
  const [who, setWho] = React.useState<Who>({ kind: "reading" });
  const [minted, setMinted] = React.useState<EnrollTokenAnswer | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [draw, setDraw] = React.useState<Draw>({ kind: "drawing" });
  const [left, setLeft] = React.useState(0);

  const start = React.useCallback(() => {
    setMinted(null);
    setRefusal(null);
    setDraw({ kind: "drawing" });
  }, []);

  // Closing drops the code from state, so a sheet opened again never shows
  // the last code.
  React.useEffect(() => { start(); }, [open, start]);

  // The console reads who is signed in on every open, so a role the
  // identity manager granted since the last open counts.
  React.useEffect(() => {
    if (!open || self) return undefined;
    let alive = true;
    setWho({ kind: "reading" });
    readSelf().then(
      (a) => { if (alive) setWho({ kind: "ready", username: a.username, mobile: (a.enroll_channels || []).includes("mobile") }); },
      (e: ApiError) => { if (alive && e.status !== 401) setWho({ kind: "error", message: readFailed(SUBJECT_SELF, e) }); },
    );
    return () => { alive = false; };
  }, [open, self]);

  // lane is the one mint the sheet offers: the self-service page's, or the
  // console person's own phone once the read says the account may enroll
  // one. The self route mints for the signed-in caller alone, so nothing
  // here names a person.
  const own = who.kind === "ready" && who.mobile ? who.username : "";
  const lane: SelfMint | null = self || (own ? {
    username: own,
    mint: async () => {
      const a = await selfEnrollToken("mobile");
      return { ...a, servers: a.servers || [], user: { id: (a.user && a.user.id) || "", username: (a.user && a.user.username) || own } };
    },
  } : null);
  const noRole = !self && who.kind === "ready" && !who.mobile;

  // The QR carries the server's own payload where it sent one, so the phone
  // reads exactly what strazad meant; the fallback is the same envelope the
  // enrolment has always used.
  React.useEffect(() => {
    if (!minted) return;
    const text = minted.qr_payload || JSON.stringify({ v: 1, servers: minted.servers, token: minted.enroll_token });
    let alive = true;
    setDraw({ kind: "drawing" });
    qrDrawing(text).then(
      (qr) => { if (alive) setDraw(qr ? { kind: "ready", qr } : { kind: "toobig", text }); },
      () => { if (alive) setDraw({ kind: "toobig", text }); },
    );
    return () => { alive = false; };
  }, [minted]);

  // The countdown runs off the deadline rather than a counter, so a tick
  // the browser skipped while the tab slept does not slow the clock.
  React.useEffect(() => {
    if (!minted) return;
    const deadline = Date.now() + minted.expires_in * 1000;
    setLeft(minted.expires_in);
    const t = setInterval(() => setLeft(Math.max(0, Math.ceil((deadline - Date.now()) / 1000))), 1000);
    return () => clearInterval(t);
  }, [minted]);

  const mint = async () => {
    if (!lane || busy) return;
    setBusy(true);
    setRefusal(null);
    try {
      const answer = await lane.mint();
      setMinted(answer);
      if (onMinted) onMinted();
    } catch (e) {
      const err = e as ApiError;
      // A refused mint never leaves a code from an earlier one on screen.
      setMinted(null);
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  // copyText says Copied in its own toast, so the button keeps one label.
  const copy = (text: string) => copyText(text, COPIED);
  // The self-service page is served beside the console, so its address
  // is this page's own origin.
  const selfService = window.location.origin + "/self-service/";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[560px]" data-enroll-sheet={minted ? "code" : "person"}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg leading-snug">{ADD_PHONE}</SheetTitle>
          <SheetDescription>{ADD_PHONE_SUB}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          {minted ? (
            <Code minted={minted} draw={draw} left={left} onCopy={copy} />
          ) : (
            <>
              <div className="flex flex-col gap-1" data-self-phone={lane ? lane.username : undefined}>
                {lane && <span className={LINE}>{selfPhoneLine(lane.username)}</span>}
                {noRole && <span className="text-[13px] leading-snug text-warn" data-no-enroll-role>{NO_ENROLL_ROLE}</span>}
                {who.kind === "error" && <FetchError subject={SUBJECT_SELF} detail={who.message} />}
                {refusal && <RefusedError subject={MINT_VERB} message={refusal} />}
              </div>
              {/* The self-service page is where everyone else enrolls, so
                  only the console says how to send someone there. */}
              {!self && (
                <Section title={FOR_OTHERS_TITLE}>
                  <span className={HINT} data-for-others>{FOR_OTHERS_LINE}</span>
                  <CopyBox text={selfService} attr="data-self-service" onCopy={() => copy(selfService)} />
                </Section>
              )}
            </>
          )}
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center justify-end gap-2">
            {minted ? (
              <>
                <Button variant="outline" onClick={start}>{CREATE_ANOTHER}</Button>
                <Button onClick={() => onOpenChange(false)}>{CLOSE}</Button>
              </>
            ) : (
              <>
                <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>{CANCEL}</Button>
                <Button disabled={!lane} aria-busy={busy || undefined} onClick={() => void mint()}>
                  {busy && <Loader2Icon className="animate-spin" />} {CREATE_QR}
                </Button>
              </>
            )}
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

// Code is the sheet after the mint: the QR, the code beside it with its
// countdown, the servers the phone tries and the transport it will get.
function Code({ minted, draw, left, onCopy }: { minted: EnrollTokenAnswer; draw: Draw; left: number; onCopy: (text: string) => void }) {
  const servers = (minted.servers || []).filter((s) => (s || "").trim() !== "");
  const trust = trustPosture(minted.servers || [], minted.tls_spki_pin);
  return (
    <>
      <div className="flex flex-wrap items-start gap-5">
        {/* The phone's camera reads the image, so the code stays black on
            white in both themes. */}
        <div className="w-[300px] max-w-full shrink-0 rounded-md border border-border bg-white p-2" data-qr-box>
          {draw.kind === "ready" && (
            <svg
              role="img"
              aria-label={QR_LABEL}
              data-qr-source={draw.qr.text}
              viewBox={"0 0 " + draw.qr.size + " " + draw.qr.size}
              shapeRendering="crispEdges"
              className="block h-auto w-full"
            >
              <rect width={draw.qr.size} height={draw.qr.size} fill="#ffffff" />
              <path d={draw.qr.path} fill="#000000" />
            </svg>
          )}
          {draw.kind === "toobig" && <p className="p-2 text-[13px] leading-snug text-danger" data-qr-problem>{QR_TOO_BIG}</p>}
        </div>

        <div className="flex min-w-[220px] flex-1 flex-col gap-4">
          <Section title={SCAN_TITLE}>
            {/* A spent code gets its own sentence, so the line never reads
                "expires in expired". */}
            {left > 0
              ? <p className={LINE} data-scan>{SCAN_LINE}<b className="font-mono font-medium text-warn" data-code-left>{codeLeft(left)}</b>.</p>
              : <p className={LINE + " text-warn"} data-scan data-code-left>{CODE_SPENT}</p>}
          </Section>

          <Section title={CODE_TITLE}>
            <CopyBox text={minted.enroll_token} attr="data-enroll-token" onCopy={() => onCopy(minted.enroll_token)} />
          </Section>
        </div>
      </div>

      {/* The approver app's Enter the code manually takes exactly the text
          the QR would have carried. */}
      {draw.kind === "toobig" && (
        <Section title={ENROLL_TEXT_TITLE}>
          <CopyBox text={draw.text} attr="data-enroll-text" onCopy={() => onCopy(draw.text)} />
        </Section>
      )}

      <Section title={SERVERS_TITLE}>
        {servers.length === 0 ? (
          <span className={HINT}>{SERVERS_NONE}</span>
        ) : (
          <ol className="list-inside list-decimal font-mono text-[13px] text-foreground" data-servers>
            {servers.map((s) => <li key={s} className="break-all">{s}</li>)}
          </ol>
        )}
      </Section>

      <Section title={TRUST_TITLE}>
        <div className="flex flex-wrap items-center gap-2">
          <ToneBadge tone={trust.tone} word={trust.word} />
          {minted.tls_spki_pin && <code className="min-w-0 break-all font-mono text-xs text-text-2" data-pin>{minted.tls_spki_pin}</code>}
        </div>
        <span className={trust.tone === "warn" ? "text-[13px] leading-snug text-warn" : HINT} data-trust-line>{trust.line}</span>
      </Section>
    </>
  );
}

// CopyBox is a value with its Copy button: the one-time code, the
// self-service address or the enrollment text. attr marks the value for
// tests and walks.
function CopyBox({ text, attr, onCopy }: { text: string; attr: string; onCopy: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-secondary/40 px-3 py-2">
      <code className="min-w-0 flex-1 select-all break-all font-mono text-[13px] text-foreground" {...{ [attr]: true }}>{text}</code>
      <Button variant="outline" size="sm" onClick={onCopy}><CopyIcon /> {COPY}</Button>
    </div>
  );
}
