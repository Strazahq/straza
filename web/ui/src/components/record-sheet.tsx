import * as React from "react";
import { CheckIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Facts, Section } from "@/components/sheet-parts";
import { PolicyTest, type PolicyTestPrefill } from "@/components/policy-test";
import { EngineFields } from "@/components/why-card";
import { COPIED, COPY_REFUSED, NOT_REHASHED, REHASHED, SENTINEL, type Row, parseCE, sevTone, shortType, whyOf } from "@/lib/audit-words";
import { TEST_THIS_CALL } from "@/lib/policy-words";
import { notify } from "@/lib/notify";
import { absTime, NONE } from "@/lib/words";
import { cn } from "@/lib/utils";

// The record sheet of the Audit screen: the one
// door on a chain record. It leads with the verdict and the call it
// decided, then the hash the record is signed by and whether this browser
// checked it, then the record as it is stored. It explains nothing the
// record does not say: every sentence on it comes from a reader in
// audit-words.ts.

// Tone is the trust vocabulary of this screen: allowed, waiting, refused,
// and a state the browser could not establish.
export type Tone = "ok" | "warn" | "danger" | "unknown";

export const TONE_TEXT: Record<Tone | "plain", string> = {
  ok: "text-ok",
  warn: "text-warn",
  danger: "text-danger",
  unknown: "text-unknown",
  plain: "text-muted-foreground",
};

const TONE_FILL: Record<Tone, string> = {
  ok: "bg-ok-bg text-ok border-ok/40",
  warn: "bg-warn-bg text-warn border-warn/40",
  danger: "bg-danger-bg text-danger border-danger/40",
  unknown: "bg-unknown-bg text-unknown border-unknown/40",
};

const TONE_STRIP: Record<Tone, string> = {
  ok: "border-ok/40 bg-ok-bg",
  warn: "border-warn/40 bg-warn-bg",
  danger: "border-danger/40 bg-danger-bg",
  unknown: "border-unknown/40 bg-unknown-bg",
};

// ToneBadge is one word of this screen's own vocabulary, a severity or a
// state, in its trust hue. The server statuses keep StatusBadge.
export function ToneBadge({ tone, className, children }: { tone: Tone; className?: string; children: React.ReactNode }) {
  return (
    <Badge variant="outline" data-tone={tone} className={cn("rounded-md px-2 font-mono text-[13px] font-normal", TONE_FILL[tone], className)}>
      {children}
    </Badge>
  );
}

// idPrefix shortens a session id to the length an operator reads, and says
// "none" where a record carries no session.
const idPrefix = (id: string) => (id ? id.slice(0, 13) + "…" : NONE);

// LANE_OF reads which lane a decision record belongs to from the tool it
// names, so Test this call opens on the lane the call was made on.
const LANE_OF: Record<string, PolicyTestPrefill["lane"]> = { "mcp.call": "mcp", "shell.exec": "shell", "net.fetch": "net" };

// testPrefill is the call a decision record holds, ready for the Test a
// call sheet. It answers null for a record that decided no call, such as an
// approval or a sentinel verdict, which have nothing to test.
function testPrefill(row: Row, ce: ReturnType<typeof parseCE>): PolicyTestPrefill | null {
  if (!ce || !whyOf(ce)) return null;
  const d = ce.data || {};
  const tool = String(d.tool || "");
  const lane = LANE_OF[tool] || (tool.startsWith("file.") ? "files" : ce.type === "straza.audit.mcp" ? "mcp" : null);
  if (!lane) return null;
  const paths = Array.isArray(d.paths) ? d.paths.map(String) : [];
  return {
    user: row.username || String(d.user || ""),
    lane,
    app: String(d.app || ""),
    tool: String(d.toolName || ""),
    command: String(d.command || ""),
    path: paths[0] || "",
  };
}

// pretty prints the record the way it is stored, indented. A record that
// does not parse is shown as the chain holds it, since that text is what
// the hash covers.
function pretty(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

type Props = {
  row: Row;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onRevoke opens the screen's confirm. The sheet never writes.
  onRevoke?: (row: Row) => void;
  revoked: boolean;
  // verified says whether the loaded chain passed verification in this browser.
  // A search result alone does not establish a chain.
  verified: boolean;
};

// RecordSheet is the door on one audit record.
export function RecordSheet({ row, open, onOpenChange, onRevoke, revoked, verified }: Props) {
  const ce = parseCE(row.raw);
  const why = whyOf(ce);
  const prefill = testPrefill(row, ce);
  const [testing, setTesting] = React.useState(false);
  const tone = sevTone(row.severity);
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  const copy = () => {
    if (!clipboard) return;
    void clipboard.writeText(row.raw).then(() => notify.ok(COPIED), () => notify.failed(COPY_REFUSED));
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto p-0 sm:max-w-2xl" data-record-sheet={row.seq}>
        <SheetHeader className="border-b border-border">
          <SheetTitle className="flex flex-wrap items-baseline gap-2 text-base">
            {"Record " + row.seq}{" "}
            <span className="font-mono text-[13px] font-normal text-muted-foreground">{shortType(row.type)}</span>
          </SheetTitle>
          <SheetDescription>
            {(row.user || NONE) + ", " + absTime(row.time) + ", session "}
            <span className="font-mono">{idPrefix(row.session)}</span>
          </SheetDescription>
        </SheetHeader>

        <div className="flex flex-col gap-5 p-4">
          {row.sentinel && (
            <div className={cn("flex flex-wrap items-center gap-2 rounded-md border px-3 py-2.5 text-sm", TONE_STRIP[tone])} data-sentinel-strip>
              <ToneBadge tone={tone}>{row.severity}</ToneBadge>
              <b className="font-mono font-semibold text-foreground">{row.detector}</b>
              <span className="font-mono text-[13px] text-muted-foreground">{idPrefix(row.session)}</span>
              <span className="text-text-2">{SENTINEL}</span>
              {row.severity === "critical" && (revoked
                ? <ToneBadge tone="danger" className="ml-auto">session revoked</ToneBadge>
                : onRevoke && <Button variant="outline" size="sm" className="ml-auto border-danger/40 text-danger hover:bg-danger-bg" onClick={() => onRevoke(row)}>Revoke session</Button>)}
            </div>
          )}

          {why && (
            <div className={cn("rounded-md border p-4", TONE_STRIP[why.tone])} data-why>
              <div className={cn("text-xl leading-tight font-semibold", TONE_TEXT[why.tone])}>{why.outcome}</div>
              {(row.tool || row.what) && (
                <p className="mt-2 font-mono text-lg leading-snug font-semibold text-foreground [overflow-wrap:anywhere]" data-call>
                  {row.tool && <span className="text-[13px] font-normal text-muted-foreground">{row.tool + " "}</span>}
                  {row.what}
                </p>
              )}
              <p className="mt-2 text-sm leading-relaxed text-text-2">{why.basis}</p>
              <p className="mt-1 text-[13px] leading-relaxed text-muted-foreground">{why.context}</p>
              <EngineFields line={why.wire} />
            </div>
          )}

          {/* The chain check is told by its fill, its mark and its words. A record this browser did not check never wears the verified tone. */}
          <Section title="Chain" className={cn("rounded-md border px-4 py-3", verified ? TONE_STRIP.ok : "border-border")}>
            <Facts
              rows={[
                ["Hash", <span className="font-mono break-all">{row.hash || NONE}</span>],
                ["Verification", verified
                  ? <span className="flex items-center gap-2 font-semibold text-ok" data-chain-check="ok"><CheckIcon className="size-4 shrink-0" aria-hidden="true" />{REHASHED}</span>
                  : <span className="text-unknown" data-chain-check="unknown">{NOT_REHASHED}</span>],
              ]}
            />
          </Section>

          <Section title="The record as stored">
            <pre className="max-h-[40vh] overflow-auto rounded-md border border-border bg-card p-3 font-mono text-[13px] leading-relaxed break-all whitespace-pre-wrap">{pretty(row.raw)}</pre>
          </Section>
        </div>

        <SheetFooter className="flex-row items-center border-t border-border">
          {prefill && <Button variant="outline" size="sm" onClick={() => setTesting(true)}>{TEST_THIS_CALL}</Button>}
          {clipboard && <Button variant="ghost" size="sm" onClick={copy}>Copy as JSON</Button>}
          <Button variant="outline" size="sm" className="ml-auto" onClick={() => onOpenChange(false)}>Close</Button>
        </SheetFooter>

        {prefill && <PolicyTest open={testing} onOpenChange={setTesting} prefill={prefill} />}
      </SheetContent>
    </Sheet>
  );
}
