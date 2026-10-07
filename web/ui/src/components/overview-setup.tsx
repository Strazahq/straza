import { CheckIcon } from "lucide-react";
import { Door, Panel } from "@/components/overview-parts";
import { SETUP, SETUP_HELP, SETUP_TITLE, type SetupStep, setupProgress } from "@/lib/config-words";
import { cn } from "@/lib/utils";

// The setup list of an empty deployment: the steps that make
// the first governed session possible, each a door to its area, each
// marked done when its object exists. It takes the place of Needs
// attention while the deployment is empty and retires on its own.

type Props = {
  // marks says which steps are done. A step whose key is absent carries no
  // mark at all, because the console cannot read that fact from here.
  marks: Partial<Record<SetupStep["key"], boolean>>;
};

// Mark is the circle at the head of a step: filled when the step's object
// exists, open while it does not, absent where nothing can be read.
function Mark({ done }: { done: boolean }) {
  return (
    <span data-mark={done ? "done" : "open"} className={cn("inline-flex size-5 shrink-0 items-center justify-center rounded-full border", done ? "border-ok bg-ok-bg text-ok" : "border-border")} aria-hidden="true">
      {done && <CheckIcon className="size-3" />}
    </span>
  );
}

// Setup renders the list with its progress count.
export function Setup({ marks }: Props) {
  const done = SETUP.filter((s) => marks[s.key] === true).length;
  return (
    <Panel
      name="setup"
      title={SETUP_TITLE}
      help={SETUP_HELP}
      lead={<span className="font-mono text-[12px] font-normal text-muted-foreground" data-setup-progress>{setupProgress(done, SETUP.length)}</span>}
      flush
    >
      {SETUP.map((s) => {
        const mark = marks[s.key];
        return (
          <div key={s.key} data-setup={s.key} data-done={mark === true ? "true" : undefined} className="flex items-center gap-3 border-b border-border px-3.5 py-2.5 text-sm last:border-b-0">
            {mark === undefined ? <span className="size-5 shrink-0" aria-hidden="true" /> : <Mark done={mark} />}
            <span className="flex min-w-0 flex-col gap-0.5">
              <b className={cn("font-semibold", mark ? "font-normal text-muted-foreground" : "text-foreground")}>{s.title}</b>
              <span className="text-[13px] text-muted-foreground">{s.line}</span>
            </span>
            <Door to={s.to} className="ml-auto" />
          </div>
        );
      })}
    </Panel>
  );
}
