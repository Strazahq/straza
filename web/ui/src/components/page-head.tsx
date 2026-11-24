import * as React from "react";
import { ArrowLeftIcon } from "lucide-react";
import { type RouteKey, landing } from "@/lib/routes";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { cn } from "@/lib/utils";

// Crumb is a page between Straza and this one in the breadcrumb, as a link.
export type Crumb = { label: string; to: RouteKey; rest?: string[] };

type Props = {
  label: string;
  // crumbs are the pages above this one, such as Users over one user.
  crumbs?: Crumb[];
  // tile leads the title: the kind tile of the object the page is about.
  tile?: React.ReactNode;
  title?: string;
  // titleExtra sits beside the title: a kind badge, a status word.
  titleExtra?: React.ReactNode;
  description: React.ReactNode;
  // descriptionExtra sits at the end of the description: the pencil that
  // opens its change dialog.
  descriptionExtra?: React.ReactNode;
  actions?: React.ReactNode;
  // sticky is false on a page whose own band sticks to the top, so the head
  // scrolls away under it.
  sticky?: boolean;
};

// PageHead opens every page the same way: the back link on a page with
// crumbs, the breadcrumb line, the h1, one sentence of description, actions
// on the right. The actions drop under the title when they do not fit
// beside it. It is sticky on long pages, and the h1 takes focus on a route
// change.
export function PageHead({ label, crumbs, tile, title, titleExtra, description, descriptionExtra, actions, sticky = true }: Props) {
  const home = pathFor(landing.key);
  // The back link leads to the last crumb, the list right above this page.
  // It goes through navigate, so the leave guard of a create flow asks first.
  const up = crumbs && crumbs.length ? crumbs[crumbs.length - 1] : null;
  return (
    <div className={cn(sticky && "sticky top-0 z-10", "border-b border-border bg-background px-6 pt-4 pb-4")} data-page-head>
      {up && (
        <a
          href={pathFor(up.to, up.rest)}
          aria-label={"Back to " + up.label}
          data-back-link
          className="flex min-h-8 w-fit max-w-full items-center gap-1.5 rounded-md text-dense font-medium text-foreground underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={(e) => {
            if (!isPlainClick(e)) return;
            e.preventDefault();
            navigate(up.to, up.rest);
          }}
        >
          <ArrowLeftIcon aria-hidden="true" className="size-4 shrink-0" />
          {up.label}
        </a>
      )}
      <nav aria-label="Breadcrumb" className="mb-1 flex flex-wrap items-center gap-x-1.5 text-caption text-muted-foreground">
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
        {(crumbs || []).map((c) => (
          <React.Fragment key={c.label}>
            <span aria-hidden="true">/</span>
            <a
              href={pathFor(c.to, c.rest)}
              className="hover:text-foreground hover:underline underline-offset-4"
              onClick={(e) => {
                if (!isPlainClick(e)) return;
                e.preventDefault();
                navigate(c.to, c.rest);
              }}
            >
              {c.label}
            </a>
          </React.Fragment>
        ))}
        <span aria-hidden="true">/</span>
        <span aria-current="page" className="min-w-0 [overflow-wrap:anywhere]">{label}</span>
      </nav>
      <div className="flex flex-wrap items-start gap-x-4 gap-y-3">
        {tile}
        <div className="min-w-0 flex-1 basis-60">
          <div className="flex flex-wrap items-center gap-2">
            <h1 tabIndex={-1} className="min-w-0 text-title leading-7 font-semibold [overflow-wrap:anywhere] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded-md">{title || label}</h1>
            {titleExtra}
          </div>
          {(description || descriptionExtra) && <p className="mt-0.5 flex max-w-[75ch] flex-wrap items-center gap-1.5 text-dense text-text-2">{description}{descriptionExtra}</p>}
        </div>
        {actions && <div data-page-actions className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}
