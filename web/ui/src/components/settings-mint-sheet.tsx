import * as React from "react";
import { CopyIcon, Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { HelpTip } from "@/components/help-tip";
import { Section } from "@/components/sheet-parts";
import { AreaTable, GrantPills, JobCards } from "@/components/settings-mint-parts";
import { GrantChips, PRE, hasScim } from "@/components/settings-token-sheet";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type ApiTokenRow, type MintedToken, createApiToken } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { CANCEL } from "@/lib/role-words";
import { refused } from "@/lib/say";
import {
  AREAS_TITLE,
  COPY,
  COPIED,
  COPY_NOW,
  DEFAULT_LIFETIME,
  DONE,
  FROM_SHELL,
  FULL_BADGE,
  FULL_TIP,
  GRANTS_MISSING,
  GRANTS_PLACEHOLDER,
  HOLDS_TITLE,
  JOBS,
  JOB_HELP,
  JOB_TITLE,
  LIFETIMES,
  LIFETIME_HINT,
  LIFETIME_LABEL,
  MINTING,
  MINTED_LEDE,
  MINT_LEDE,
  MINT_TITLE,
  MINT_VERB,
  NAME_FREE,
  NAME_HINT,
  NAME_LABEL,
  NAME_MISSING,
  NEVER_WARN,
  ROOT_WARN,
  SCOPE_LINE,
  SCOPE_TITLE,
  SHOWN_ONCE,
  USE_TITLE,
  canonicalScope,
  curlLine,
  expiresLine,
  expiresWord,
  headerLine,
  mintedTitle,
  nameTaken,
  scopeLine,
  useLine,
} from "@/lib/settings-words";
import { dayOf } from "@/lib/words";

// The mint sheet: the name with a live
// check against the loaded rows, the lifetime, the job as a card, the
// grants that follow it, and the canonical scope the server is sent. Once
// the server answers, the same sheet becomes the one look at the secret.

const HINT = "text-[13px] leading-snug text-muted-foreground";
const ERROR = "text-[13px] leading-snug text-danger";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // rows are the tokens already on screen, which the name check reads.
  rows: ApiTokenRow[];
  // onMinted tells the tab a token landed, so the table reloads.
  onMinted: () => void;
};

export function MintSheet({ open, onOpenChange, rows, onMinted }: Props) {
  const [name, setName] = React.useState("");
  const [lifetime, setLifetime] = React.useState(DEFAULT_LIFETIME);
  const [job, setJob] = React.useState("");
  const [grants, setGrants] = React.useState<string[]>([]);
  const [full, setFull] = React.useState(false);
  const [nameMiss, setNameMiss] = React.useState(false);
  const [grantsMiss, setGrantsMiss] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [minted, setMinted] = React.useState<MintedToken | null>(null);
  const [busy, setBusy] = React.useState(false);
  const nameRef = React.useRef<HTMLInputElement>(null);

  // Every open starts from the same sheet: no name, the 90 day lifetime,
  // no job, and no secret left from the last mint.
  React.useEffect(() => {
    if (!open) return;
    setName("");
    setLifetime(DEFAULT_LIFETIME);
    setJob("");
    setGrants([]);
    setFull(false);
    setNameMiss(false);
    setGrantsMiss(false);
    setRefusal(null);
    setMinted(null);
  }, [open]);

  const picked = JOBS.find((j) => j.key === job);
  const trimmed = name.trim();
  const taken = trimmed !== "" && rows.some((r) => r.name === trimmed);
  const scope = canonicalScope(full, grants);
  const root = full || grants.includes("tokens:write");

  const pickJob = (key: string) => {
    const next = JOBS.find((j) => j.key === key);
    setJob(key);
    setGrantsMiss(false);
    if (!next) return;
    if (next.full) {
      setFull(true);
      setGrants([]);
      return;
    }
    setFull(false);
    // Custom keeps what is held, which is how a preset's leftovers reach
    // the area table; a preset replaces the set with its own.
    if (next.why) setGrants(Object.keys(next.why).sort());
  };

  const mint = async () => {
    if (busy) return;
    if (!trimmed) {
      setNameMiss(true);
      nameRef.current?.focus();
      return;
    }
    if (!full && grants.length === 0) {
      setGrantsMiss(true);
      return;
    }
    setBusy(true);
    setRefusal(null);
    try {
      const answer = await createApiToken(trimmed, scope, lifetime);
      setMinted(answer);
      onMinted();
    } catch (e) {
      const err = e as ApiError;
      // A refused mint never leaves a secret from an earlier one on screen.
      setMinted(null);
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[760px]" data-mint-sheet={minted ? "minted" : "form"}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg leading-snug">{minted ? mintedTitle(minted.name) : MINT_TITLE}</SheetTitle>
          <SheetDescription>{minted ? MINTED_LEDE : MINT_LEDE}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          {minted ? <Minted minted={minted} /> : (
            <>
              <div className="flex flex-col gap-1">
                <Label htmlFor="token-name">{NAME_LABEL}</Label>
                <Input
                  id="token-name"
                  ref={nameRef}
                  value={name}
                  className="max-w-sm"
                  onChange={(e) => { setName(e.target.value); setNameMiss(false); setRefusal(null); }}
                />
                <span className={HINT}>{NAME_HINT}</span>
                {refusal ? <span className={ERROR} role="alert" data-mint-refused>{refusal}</span>
                  : nameMiss ? <span className={ERROR} role="alert">{NAME_MISSING}</span>
                    : taken ? <span className={ERROR}>{nameTaken(trimmed)}</span>
                      : trimmed ? <span className="text-[13px] leading-snug text-ok">{NAME_FREE}</span>
                        : null}
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="token-lifetime">{LIFETIME_LABEL}</Label>
                <Select value={String(lifetime)} onValueChange={(v) => setLifetime(Number(v))}>
                  <SelectTrigger id="token-lifetime" aria-label={LIFETIME_LABEL} className="w-[240px]"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {LIFETIMES.map((l) => <SelectItem key={l.seconds} value={String(l.seconds)}>{l.label}</SelectItem>)}
                  </SelectContent>
                </Select>
                <span className={HINT}>{LIFETIME_HINT}</span>
                {lifetime === 0 && <span className="text-[13px] leading-snug text-warn" data-never-warn>{NEVER_WARN}</span>}
              </div>

              <Section title={JOB_TITLE} action={<HelpTip label={JOB_TITLE} text={JOB_HELP} />}>
                <JobCards picked={job} onPick={pickJob} />
                {grantsMiss && <span className={ERROR} role="alert">{GRANTS_MISSING}</span>}
                {full ? <WordBadge word={FULL_BADGE} tone="warn" title={FULL_TIP} attr="data-grant" />
                  : picked && picked.custom ? null
                    : grants.length > 0 ? <GrantPills grants={grants} why={(picked && picked.why) || {}} onRemove={(g) => setGrants(grants.filter((x) => x !== g))} />
                      : <span className={HINT}>{GRANTS_PLACEHOLDER}</span>}
              </Section>

              {picked && picked.custom && (
                <Section title={AREAS_TITLE}>
                  <AreaTable grants={grants} onChange={setGrants} />
                </Section>
              )}

              {(full || grants.length > 0) && (
                <Section title={SCOPE_TITLE}>
                  <pre className={PRE} data-scope>{scopeLine(scope)}</pre>
                  <span className={HINT}>{SCOPE_LINE}</span>
                  {root && <span className="text-[13px] leading-snug text-warn" data-root-warn>{ROOT_WARN}</span>}
                </Section>
              )}
            </>
          )}
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center justify-end gap-2">
            {minted ? (
              <Button onClick={() => onOpenChange(false)}>{DONE}</Button>
            ) : (
              <>
                <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>{CANCEL}</Button>
                <Button onClick={() => void mint()} aria-busy={busy || undefined}>
                  {busy && <Loader2Icon className="animate-spin" />} {busy ? MINTING : MINT_VERB}
                </Button>
              </>
            )}
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

// Minted is the one look at the secret: the value with its copy button,
// the two lines a caller signs with, and what the token holds.
function Minted({ minted }: { minted: MintedToken }) {
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  const copy = () => copyText(minted.token, COPIED);
  return (
    <>
      <div className="flex flex-col gap-2 rounded-md border border-warn/40 bg-warn-bg px-3 py-3" data-secret>
        <div className="flex flex-wrap items-center gap-2">
          <WordBadge word={SHOWN_ONCE} tone="warn" />
          <code className="min-w-0 flex-1 break-all font-mono text-[13px] text-foreground">{minted.token}</code>
          <Button variant="outline" size="sm" onClick={copy}><CopyIcon /> {COPY}</Button>
        </div>
        <span className="text-[13px] leading-snug text-text-2">{COPY_NOW}</span>
      </div>

      <Section title={USE_TITLE}>
        <span className="text-[13px] leading-snug text-text-2">{useLine(hasScim(minted.scope))}</span>
        <pre className={PRE}>{headerLine(minted.token)}</pre>
        <span className="text-[13px] leading-snug text-text-2">{FROM_SHELL}</span>
        <pre className={PRE}>{curlLine(origin)}</pre>
      </Section>

      <Section title={HOLDS_TITLE}>
        <GrantChips scope={minted.scope} />
        <span className={HINT} data-expires-line>
          {minted.expires ? expiresLine(expiresWord(minted.expires), dayOf(minted.expires)) : NEVER_WARN}
        </span>
      </Section>
    </>
  );
}
