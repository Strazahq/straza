import * as React from "react";
import { Button } from "@/components/ui/button";
import type { DraftSummary } from "@/lib/api";
import { listDrafts } from "@/lib/drafts-api";
import { navigate } from "@/lib/router";
import { DIFFERS, differsSince, fileWaits, lastPublished, openWaits, originFile, reviewDraft } from "@/lib/save-words";
import { fileParts } from "@/lib/server-words";
import { adminAreas } from "@/lib/session";
import { absTime } from "@/lib/words";

// The origin line of a server's, a role's and a policy's page: the apps
// directory file a server came from and whether live
// differs from it, the draft that last published the object, and the
// drafts that wait to change it. A file owns nothing, so this is a line
// and not a lock. Every read fails soft into a shorter line.

type Props = {
  // object is Kind/Name, as the drafts list filters on it.
  object: string;
  name: string;
  // file and differs are the server row's: the present apps directory file
  // that names it, and whether live differs from that file.
  file?: string;
  differs?: boolean;
};

type Read = { last: DraftSummary | null; open: DraftSummary[] };

// PAGE is how many published drafts the line reads to find the newest
// publish, since the list is in id order and a draft made earlier can be
// published later.
const PAGE = 20;

const soft = (p: Promise<{ items: DraftSummary[] }>) => p.then((page) => page.items || [], () => [] as DraftSummary[]);
const newest = (rows: DraftSummary[]) => rows.reduce<DraftSummary | null>((best, d) => (!best || (d.decided_at || "") > (best.decided_at || "") ? d : best), null);

export function OriginLine({ object, name, file, differs }: Props) {
  const [read, setRead] = React.useState<Read | null>(null);
  // A reader without root or drafts:read lists only the drafts it wrote or
  // whose every item is its own server's, so its newest
  // publish could be an older one: it reads no last publish and no count.
  const areas = adminAreas();
  const whole = areas === null || !!areas.drafts;

  React.useEffect(() => {
    let alive = true;
    const q = (state: string) => "state=" + state + "&object=" + encodeURIComponent(object) + "&limit=" + PAGE;
    void Promise.all([whole ? soft(listDrafts(q("published"))) : Promise.resolve([]), soft(listDrafts(q("open")))]).then(([pub, open]) => {
      if (alive) setRead({ last: newest(pub), open });
    });
    return () => { alive = false; };
  }, [object, whole]);

  const last = read ? read.last : null;
  const fileDraft = read && file ? read.open.find((d) => d.door === "apps-directory" && d.source === file) || null : null;
  const others = read && whole ? read.open.filter((d) => d !== fileDraft) : [];
  const published = last ? lastPublished(last.decided_by?.username || "", absTime(last.decided_at), last.id) : "";

  let head = "";
  const lines: string[] = [];
  if (file) {
    const { base, dir } = fileParts(file);
    head = originFile(base, dir);
    if (differs) lines.push(last && last.door !== "apps-directory" ? differsSince(last.decided_by?.username || "", last.id, absTime(last.decided_at)) : DIFFERS);
    else if (published) lines.push(published);
  } else if (published) {
    head = published;
  }
  if (fileDraft) lines.push(fileWaits(fileDraft.id));
  if (others.length) lines.push(openWaits(others.length, others[0].id, name));
  if (!head) head = lines.shift() || "";
  if (!head) return null;

  const doors = [fileDraft, others[0]].filter((d): d is DraftSummary => !!d);
  return (
    <div className="flex flex-col items-start gap-1 rounded-md border border-border bg-card px-3 py-2.5 text-sm leading-relaxed text-text-2" data-origin>
      <p className="m-0 max-w-[75ch]"><b className="font-semibold text-foreground">{head}</b></p>
      {lines.map((l) => <p key={l} className="m-0 max-w-[75ch]">{l}</p>)}
      {doors.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-2">
          {doors.map((d) => <Button key={d.id} variant="outline" size="sm" onClick={() => navigate("drafts", [d.id])}>{reviewDraft(d.id)}</Button>)}
        </div>
      )}
    </div>
  );
}
