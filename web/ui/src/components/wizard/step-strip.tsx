import { CheckIcon } from "lucide-react";
import { cn } from "@/lib/utils";

// StepStrip is the wizard's step list on top: done steps
// carry a check mark, the current step is marked for assistive technology
// with aria-current, the rest are muted. labels are the steps in order and
// at is the index of the current one.
export function StepStrip({ labels, at }: { labels: string[]; at: number }) {
  return (
    <ol aria-label="wizard steps" className="m-0 flex list-none flex-wrap gap-0.5 border-b border-border p-0">
      {labels.map((label, i) => {
        const done = i < at;
        const current = i === at;
        return (
          <li
            key={label}
            aria-current={current ? "step" : undefined}
            className={cn(
              "-mb-px flex items-center gap-2 border-b-2 px-3 py-2 text-sm",
              current ? "border-link font-semibold text-link" : "border-transparent",
              done ? "text-foreground" : !current && "text-muted-foreground",
            )}
          >
            <span aria-hidden="true" className={cn("font-mono text-xs", done && "text-ok")}>{done ? <CheckIcon className="size-3.5" /> : i + 1}</span>
            {label}
          </li>
        );
      })}
    </ol>
  );
}
