import type * as React from "react";

type Props = {
  title: string;
  // extra follows the title in the card's head: a muted word, a filter, a
  // button.
  extra?: React.ReactNode;
  children: React.ReactNode;
  [key: `data-${string}`]: string | boolean | undefined;
};

// SessionCard is one card of a session's page: a head with the title in
// the section size, and a body.
export function SessionCard({ title, extra, children, ...data }: Props) {
  return (
    <section className="flex min-w-0 flex-col rounded-md border border-border bg-card" {...data}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-2 border-b border-border px-4 py-3">
        <h2 className="m-0 text-section leading-7 font-semibold text-foreground">{title}</h2>
        {extra}
      </div>
      <div className="flex min-w-0 flex-col gap-3 px-4 py-3">{children}</div>
    </section>
  );
}
