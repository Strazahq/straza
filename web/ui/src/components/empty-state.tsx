import type * as React from "react";
import type { LucideIcon } from "lucide-react";

type Props = {
  icon: LucideIcon;
  title: string;
  children: React.ReactNode;
  action?: React.ReactNode;
};

// EmptyState says what would be here, why it is empty, and the one action
// that fills it, in that order: a muted icon, a title, one
// or two sentences, and at most one action.
export function EmptyState({ icon: Icon, title, children, action }: Props) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center" data-empty-state>
      <Icon className="size-6 text-muted-foreground" aria-hidden="true" />
      <p className="font-semibold text-foreground">{title}</p>
      <p className="max-w-[60ch] text-sm leading-relaxed text-text-2">{children}</p>
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}
