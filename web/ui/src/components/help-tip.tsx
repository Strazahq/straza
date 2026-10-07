import { InfoIcon } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

type Props = {
  // label names what the sentence explains: a column, a badge, a verb. It
  // becomes the accessible name "Help: <label>".
  label: string;
  text: string;
  className?: string;
};

// HelpTip is the help icon beside a label, a column header or a line: a
// small circled i whose tooltip carries the one sentence that explains the
// thing, so the sentence stays off the surface and dialog bodies stay
// short. A click never reaches the row behind it.
export function HelpTip({ label, text, className }: Props) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={"Help: " + label}
          data-help={label}
          className={cn("inline-flex size-4 shrink-0 items-center justify-center rounded-full align-middle text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50", className)}
          onClick={(e) => e.stopPropagation()}
        >
          <InfoIcon className="size-3.5" aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[44ch] text-[13px] leading-snug">{text}</TooltipContent>
    </Tooltip>
  );
}
