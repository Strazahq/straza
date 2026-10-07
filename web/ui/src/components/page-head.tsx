import type * as React from "react";
import { landing } from "@/lib/routes";
import { isPlainClick, navigate, pathFor } from "@/lib/router";

type Props = {
  label: string;
  title?: string;
  // titleExtra sits beside the title: a kind badge, a status word.
  titleExtra?: React.ReactNode;
  description: string;
  // descriptionExtra sits at the end of the description: the pencil that
  // opens its change dialog.
  descriptionExtra?: React.ReactNode;
  actions?: React.ReactNode;
};

// PageHead opens every page the same way: the breadcrumb
// line, the h1, one sentence of description, actions on the right. It is
// sticky on long pages, and the h1 takes focus on a route change.
export function PageHead({ label, title, titleExtra, description, descriptionExtra, actions }: Props) {
  const home = pathFor(landing.key);
  return (
    <div className="sticky top-0 z-10 border-b border-border bg-background px-6 pt-4 pb-4" data-page-head>
      <nav aria-label="Breadcrumb" className="mb-1 flex items-center gap-1.5 text-[13px] text-muted-foreground">
        <a
          href={home}
          className="hover:text-foreground hover:underline underline-offset-4"
          onClick={(e) => {
            if (!isPlainClick(e)) return;
            e.preventDefault();
            navigate(landing.key);
          }}
        >
          Straza
        </a>
        <span aria-hidden="true">/</span>
        <span aria-current="page">{label}</span>
      </nav>
      <div className="flex items-start gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h1 tabIndex={-1} className="text-xl font-semibold text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded-sm">{title || label}</h1>
            {titleExtra}
          </div>
          {(description || descriptionExtra) && <p className="mt-0.5 flex max-w-[75ch] flex-wrap items-center gap-1.5 text-sm text-text-2">{description}{descriptionExtra}</p>}
        </div>
        {actions && <div data-page-actions className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}
