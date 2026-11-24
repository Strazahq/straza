import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Draft } from "@/lib/api";
import { objectOf } from "@/lib/drafts-model";
import { copyText } from "@/lib/clipboard";
import { CLOSE, COPY, FILE_MASKED, YAML_COPIED, YAML_SUB, yamlName } from "@/lib/drafts-words";

// The YAML sheet of a draft's page: one tab per item that has a file, each
// the document as the draft holds it, which a person can copy. A server's
// file says that the values the server keeps secret read masked.

type Props = {
  draft: Draft;
  open: boolean;
  // shown is the Kind/Name of the item whose tab shows, since a server and a
  // role may share a name. It stays while the sheet closes, so its title
  // does not change on the way out.
  shown: string;
  onShow: (name: string) => void;
  onClose: () => void;
  // onClosed runs once the sheet is gone, so the page can put focus back on
  // the button that opened it.
  onClosed: () => void;
};

export function DraftYaml({ draft, open, shown, onShow, onClose, onClosed }: Props) {
  const files = draft.items.filter((it) => it.doc && !it.withheld);
  const current = files.find((it) => objectOf(it) === shown);
  return (
    <Sheet open={open && !!current} onOpenChange={(o) => { if (!o) onClose(); }}>
      {/* The width is set inline, because the console's sheet rule caps every sheet at 720 px. */}
      <SheetContent
        className="w-full gap-0 p-0"
        style={{ maxWidth: 640 }}
        showCloseButton={false}
        onCloseAutoFocus={(e) => { e.preventDefault(); onClosed(); }}
        data-yaml-sheet={shown}
      >
        <SheetHeader className="border-b border-border">
          <SheetTitle className="font-mono text-section leading-7 break-all">{yamlName(current?.name || "")}</SheetTitle>
          <SheetDescription>{YAML_SUB[draft.state]}</SheetDescription>
        </SheetHeader>
        <Tabs value={shown} onValueChange={onShow} className="flex min-h-0 flex-1 flex-col gap-3 p-4">
          <div className="max-w-full overflow-x-auto border-b border-border">
            <TabsList variant="line">
              {files.map((it) => <TabsTrigger key={objectOf(it)} value={objectOf(it)} className="font-mono">{yamlName(it.name)}</TabsTrigger>)}
            </TabsList>
          </div>
          {files.map((it) => (
            <TabsContent key={objectOf(it)} value={objectOf(it)} className="flex min-h-0 flex-col gap-2">
              <pre className="m-0 min-h-0 flex-1 overflow-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-caption leading-relaxed text-foreground" data-yaml={objectOf(it)}>{it.doc}</pre>
              {it.kind === "App" && <p className="m-0 text-caption text-muted-foreground">{FILE_MASKED}</p>}
            </TabsContent>
          ))}
        </Tabs>
        <SheetFooter className="flex-row justify-end border-t border-border">
          <Button variant="ghost" onClick={() => copyText(current?.doc || "", YAML_COPIED)}>{COPY}</Button>
          <Button variant="outline" onClick={onClose}>{CLOSE}</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
