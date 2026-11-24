import { useRef, useState } from "react";
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
  // hover is the tooltip's own open state, from hover and focus. pinned is
  // a click or a tap, the only way in on a phone; Escape, a pointer down
  // outside the tip and the icon losing focus let go of it.
  const [hover, setHover] = useState(false);
  const [pinned, setPinned] = useState(false);
  const icon = useRef<HTMLButtonElement>(null);
  const open = hover || pinned;
  // A pointer down on the icon itself is the click that toggles, not one
  // outside.
  const outside = (e: Event) => {
    if (!icon.current?.contains(e.target as Node)) setPinned(false);
  };
  return (
    <Tooltip open={open} onOpenChange={setHover}>
      <TooltipTrigger asChild>
        <button
          ref={icon}
          type="button"
          aria-label={"Help: " + label}
          aria-expanded={open}
          data-help={label}
          className={cn("inline-flex size-4 shrink-0 items-center justify-center rounded-full align-middle text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50", className)}
          onClick={(e) => { e.stopPropagation(); setPinned((p) => !p); }}
          onBlur={() => setPinned(false)}
        >
          <InfoIcon className="size-3.5" aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[44ch] text-caption leading-snug" onEscapeKeyDown={() => setPinned(false)} onPointerDownOutside={outside}>
        {text}
      </TooltipContent>
    </Tooltip>
  );
}
