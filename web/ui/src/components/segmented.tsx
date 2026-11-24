import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

// Data is the data-* attributes a caller hands through, to the group or to
// one option's button, the hooks that tests read.
type Data = { [key: `data-${string}`]: string | number | boolean | undefined };

type SegmentedOption<T extends string> = Data & { value: T; label: string; icon?: LucideIcon; count?: number | string; title?: string; disabled?: boolean };

type Props<T extends string> = Data & {
  label: string;
  // value is the pressed option; null presses none, for a choice not made
  // yet.
  value: T | null;
  onChange: (value: T) => void;
  options: SegmentedOption<T>[];
  className?: string;
};

// Segmented is a single choice between views or filters: a row of joined
// plain buttons with aria-pressed. It is not built on the outline Button,
// whose dark-theme fill and border outrank the pressed state.
export function Segmented<T extends string>({ label, value, onChange, options, className, ...data }: Props<T>) {
  return (
    <div role="group" aria-label={label} className={cn("inline-flex w-fit max-w-full overflow-x-auto rounded-md border border-input bg-card", className)} {...data}>
      {options.map(({ value: v, label: l, icon: Icon, count, title, disabled, ...attrs }) => (
        <button
          key={v}
          type="button"
          aria-pressed={v === value}
          title={title}
          disabled={disabled}
          onClick={() => onChange(v)}
          className={cn(
            "inline-flex items-center gap-1.5 border-l border-border px-3 py-1 text-dense whitespace-nowrap text-text-2 outline-none first:border-l-0 focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:ring-inset disabled:opacity-50",
            v === value && "bg-accent-bg font-semibold text-link",
          )}
          {...attrs}
        >
          {Icon && <Icon className="size-4 shrink-0" aria-hidden="true" />}
          {l}
          {/* The space keeps the label and the count apart in the button's name. */}
          {count !== undefined && <> <span className="font-mono">{count}</span></>}
        </button>
      ))}
    </div>
  );
}
