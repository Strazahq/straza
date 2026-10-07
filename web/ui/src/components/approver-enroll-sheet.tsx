import * as React from "react";
import { CopyIcon, Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RefusedError } from "@/components/error-state";
import { ToneBadge } from "@/components/overview-parts";
import { Section } from "@/components/sheet-parts";
import { type PickedUser, UserPicker } from "@/components/user-picker";
import { type ApiError, type EnrollTokenAnswer, getUser, mintEnrollToken } from "@/lib/api";
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
  MINT_VERB,
  PERSON_HINT,
  PERSON_LABEL,
  QR_LABEL,
  QR_TOO_BIG,
  SCAN_LINE,
  SCAN_TITLE,
  SERVERS_NONE,
  SERVERS_TITLE,
  TRUST_TITLE,
  agentPicked,
  codeLeft,
  selfPhoneLine,
  trustPosture,
} from "@/lib/approval-words";
import { copyText } from "@/lib/clipboard";
import { type QrDrawing, qrDrawing } from "@/lib/qr";
import { refused } from "@/lib/say";
import { kindWord } from "@/lib/user-words";

// EnrollSheet is Add a phone: pick the person, mint
// the one-time code, and show the QR the Straza approver app scans beside
// the code itself, the servers the phone will try and how it will trust
// them. The code lives in this component's state alone and goes when the
// sheet closes; nothing writes it anywhere.

const HINT = "text-[13px] leading-snug text-muted-foreground";
const LINE = "text-[13px] leading-snug text-text-2";

// SelfMint is the self-service page's lane: the person is the signed-in one
// and the mint is their own, so the sheet skips the picker and the person
// check, and everything after the mint is the console's.
export type SelfMint = { username: string; mint: () => Promise<EnrollTokenAnswer> };

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onMinted says a code was minted. The tab reads the list again, so a
  // device enrolled since the last poll is on screen while the operator
  // watches for the new one.
  onMinted?: () => void;
  // self hands in the person's own mint; the console names none and picks.
  self?: SelfMint;
};

// Draw is the QR's own state: the encoder loads on first use, and a text no
// QR code can carry is said in words rather than drawn.
type Draw = { kind: "drawing" } | { kind: "ready"; qr: QrDrawing } | { kind: "toobig" };

export function EnrollSheet({ open, onOpenChange, onMinted, self }: Props) {
  const [picked, setPicked] = React.useState<PickedUser | null>(null);
  const [checking, setChecking] = React.useState(false);
  const [notPerson, setNotPerson] = React.useState(false);
  const [minted, setMinted] = React.useState<EnrollTokenAnswer | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [draw, setDraw] = React.useState<Draw>({ kind: "drawing" });
  const [left, setLeft] = React.useState(0);
  const seq = React.useRef(0);

  const start = React.useCallback(() => {
    setPicked(null);
    setChecking(false);
    setNotPerson(false);
    setMinted(null);
    setRefusal(null);
    setDraw({ kind: "drawing" });
    seq.current++;
  }, []);

  // Every open starts from an empty picker, and closing drops the code from
  // state, so a sheet opened again never shows the last person's code.
  React.useEffect(() => { start(); }, [open, start]);

  // The QR carries the server's own payload where it sent one, so the phone
  // reads exactly what strazad meant; the fallback is the same envelope the
  // enrolment has always used.
  React.useEffect(() => {
    if (!minted) return;
    const text = minted.qr_payload || JSON.stringify({ v: 1, servers: minted.servers, token: minted.enroll_token });
    let alive = true;
    setDraw({ kind: "drawing" });
    qrDrawing(text).then(
      (qr) => { if (alive) setDraw(qr ? { kind: "ready", qr } : { kind: "toobig" }); },
      () => { if (alive) setDraw({ kind: "toobig" }); },
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

  const pick = (u: PickedUser | null) => {
    setPicked(u);
    setNotPerson(false);
    setRefusal(null);
    const my = ++seq.current;
    if (!u) { setChecking(false); return; }
    // The picker answers with the id and the name alone, so what the
    // identity is comes from its own record. A record that cannot be read
    // leaves the button enabled, since the server refuses an agent itself.
    setChecking(true);
    getUser(u.id).then(
      (d) => { if (my === seq.current) { setNotPerson(kindWord(d) !== "person"); setChecking(false); } },
      () => { if (my === seq.current) setChecking(false); },
    );
  };

  const mint = async () => {
    if ((!picked && !self) || busy) return;
    setBusy(true);
    setRefusal(null);
    try {
      const answer = self ? await self.mint() : await mintEnrollToken(picked ? picked.id : "");
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
  const copy = (token: string) => copyText(token, COPIED);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[560px]" data-enroll-sheet={minted ? "code" : "person"}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg leading-snug">{ADD_PHONE}</SheetTitle>
          <SheetDescription>{ADD_PHONE_SUB}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          {minted ? (
            <Code minted={minted} draw={draw} left={left} onCopy={() => copy(minted.enroll_token)} />
          ) : self ? (
            <div className="flex flex-col gap-1" data-self-phone={self.username}>
              <span className={LINE}>{selfPhoneLine(self.username)}</span>
              {refusal && <RefusedError subject={MINT_VERB} message={refusal} />}
            </div>
          ) : (
            <div className="flex flex-col gap-1">
              <UserPicker value={picked} onChange={pick} label={PERSON_LABEL} />
              <span className={HINT}>{PERSON_HINT}</span>
              {notPerson && picked && <span className="text-[13px] leading-snug text-danger" role="alert" data-agent-picked>{agentPicked(picked.username)}</span>}
              {refusal && <RefusedError subject={MINT_VERB} message={refusal} />}
            </div>
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
                <Button disabled={self ? false : (!picked || checking || notPerson)} aria-busy={busy || undefined} onClick={() => void mint()}>
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
function Code({ minted, draw, left, onCopy }: { minted: EnrollTokenAnswer; draw: Draw; left: number; onCopy: () => void }) {
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
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-secondary/40 px-3 py-2">
              <code className="min-w-0 flex-1 select-all break-all font-mono text-[13px] text-foreground" data-enroll-token>{minted.enroll_token}</code>
              <Button variant="outline" size="sm" onClick={onCopy}><CopyIcon /> {COPY}</Button>
            </div>
          </Section>
        </div>
      </div>

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
