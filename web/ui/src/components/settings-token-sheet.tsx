import type * as React from "react";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { Facts, Section } from "@/components/sheet-parts";
import { WordBadge } from "@/components/users-table";
import type { ApiTokenRow } from "@/lib/api";
import { CLOSE } from "@/lib/role-words";
import {
  FULL_BADGE,
  FULL_TIP,
  GRANTS_TITLE,
  MINTED_BY,
  NEVER,
  NOT_SET,
  REVOKE_VERB,
  SENSITIVE,
  TOKEN_COLUMN,
  TOKEN_ELIDED,
  TOKEN_KIND,
  USE_TITLE,
  areaHint,
  expiresWord,
  grantsOf,
  headerLine,
  useLine,
} from "@/lib/settings-words";
import { absTime, relTimeText } from "@/lib/words";

// A token's own sheet: the facts of
// the credential, every grant beside the sentence of the area it reaches,
// how a caller signs with it, and Revoke alone on the left of the footer.

const DANGER_BUTTON = "border-danger/40 text-danger hover:bg-danger-bg hover:text-danger";
export const PRE = "overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[13px] text-foreground";

// GrantChips renders a token's scope as one chip per grant, the sensitive
// ones in amber with their reason on hover, and a full scope as the one
// root badge.
export function GrantChips({ scope }: { scope: string }) {
  if (scope === "full") return <WordBadge word={FULL_BADGE} tone="warn" title={FULL_TIP} attr="data-grant" />;
  return (
    <span className="flex flex-wrap gap-1">
      {grantsOf(scope).map((g) => (
        <WordBadge key={g} word={g} tone={SENSITIVE[g] ? "warn" : "plain"} mono title={SENSITIVE[g]} attr="data-grant" />
      ))}
    </span>
  );
}

// hasScim says whether a caller can use this token on the SCIM plane, the
// one grant that changes the sentence on how it is used.
export const hasScim = (scope: string) => scope === "full" || grantsOf(scope).some((g) => g.startsWith("scim:"));

type Props = {
  token: ApiTokenRow;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onRevoke hands the row back to the tab, which owns the dialog.
  onRevoke: (token: ApiTokenRow) => void;
};

export function TokenSheet({ token, open, onOpenChange, onRevoke }: Props) {
  const muted = (text: string) => <span className="text-muted-foreground">{" (" + text + ")"}</span>;
  const facts: [string, React.ReactNode][] = [
    [MINTED_BY, token.created_by ? <span className="font-mono">{token.created_by}</span> : <span className="text-muted-foreground">{NOT_SET}</span>],
    [TOKEN_COLUMN.created, <span className="tabular-nums">{absTime(token.created)}{muted(relTimeText(token.created))}</span>],
    [TOKEN_COLUMN.expires, token.expires
      ? <span className="tabular-nums">{absTime(token.expires)}{muted(expiresWord(token.expires))}</span>
      : <span className="text-warn">{NEVER}</span>],
    [TOKEN_COLUMN.used, token.lastUsed
      ? <span className="tabular-nums">{absTime(token.lastUsed)}{muted(relTimeText(token.lastUsed))}</span>
      : <span className="text-muted-foreground">{NEVER}</span>],
  ];
  const grants = grantsOf(token.scope);

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[560px]" data-token-sheet={token.name}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="font-mono text-lg leading-snug">{token.name}</SheetTitle>
          <SheetDescription>{TOKEN_KIND}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          <Facts rows={facts} />

          <Section title={GRANTS_TITLE}>
            {token.scope === "full" ? (
              <p className="flex flex-wrap items-center gap-2 text-sm text-text-2">
                <WordBadge word={FULL_BADGE} tone="warn" attr="data-grant" />
                {FULL_TIP}
              </p>
            ) : (
              <div className="flex flex-col gap-2">
                {grants.map((g) => {
                  const area = g.split(":")[0];
                  const sensitive = SENSITIVE[g];
                  return (
                    <div key={g} className="flex flex-col gap-0.5" data-grant-line={g}>
                      <b className={"font-mono text-[13px] font-semibold " + (sensitive ? "text-warn" : "text-foreground")}>{g}</b>
                      <span className="text-[13px] leading-snug text-text-2">{areaHint(area) + "." + (sensitive ? " " + sensitive : "")}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </Section>

          <Section title={USE_TITLE}>
            <pre className={PRE}>{headerLine(TOKEN_ELIDED)}</pre>
            <span className="text-[13px] leading-snug text-text-2">{useLine(hasScim(token.scope))}</span>
          </Section>
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center gap-2">
            <Button variant="outline" className={DANGER_BUTTON} onClick={() => onRevoke(token)}>{REVOKE_VERB}</Button>
            <Button variant="outline" className="ml-auto" onClick={() => onOpenChange(false)}>{CLOSE}</Button>
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
