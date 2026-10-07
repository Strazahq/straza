// usePagedList reads one paged admin list (the {items, next_cursor}
// envelope) the way the Users, Sessions, Audit and Transcripts screens do:
// the filters and the sort as query parameters, the first page on every
// change of them, Load more through the cursor, an optional poll of the
// first page, and a ready state that keeps its rows through a failed reload.
import * as React from "react";
import { type ApiError, type Page, type SortOrder, query } from "./api";
import { readFailed } from "./say";

export type Sorting = { key: string; order: SortOrder };

export type PagedState<T> =
  | { kind: "loading" }
  | { kind: "ready"; rows: T[]; next: string; lastRead: Date; problem: string | null; more: boolean }
  | { kind: "error"; message: string };

type Options<T> = {
  // read fetches one page from the query string the hook builds.
  read: (q: string) => Promise<Page<T>>;
  // subject names the list in the error sentences ("The user list").
  subject: string;
  // params are the filters; an empty value is left out of the query.
  params: Record<string, string | undefined>;
  sorting: Sorting | null;
  limit?: number;
  // pollMs re-reads the first page on a timer while no older page is
  // loaded, so a walked tail is never overwritten under the person.
  pollMs?: number;
};

export function usePagedList<T>({ read, subject, params, sorting, limit = 100, pollMs }: Options<T>) {
  const [state, setState] = React.useState<PagedState<T>>({ kind: "loading" });
  const [busy, setBusy] = React.useState(false);
  const walked = React.useRef(false);
  const seq = React.useRef(0);
  const key = JSON.stringify([params, sorting]);

  const base = React.useCallback((cursor: string) => query({ ...params, sort: sorting?.key, order: sorting?.order, limit, cursor }), [key, limit]); // eslint-disable-line react-hooks/exhaustive-deps

  const load = React.useCallback(async (mode: "first" | "poll" | "more") => {
    const my = ++seq.current;
    if (mode === "first") setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    if (mode === "more") setBusy(true);
    try {
      const cursor = mode === "more" ? (state.kind === "ready" ? state.next : "") : "";
      const page = await read(base(cursor));
      if (my !== seq.current) return;
      const items = page.items || [];
      setState((s) => {
        const rows = mode === "more" && s.kind === "ready" ? [...s.rows, ...items] : items;
        return { kind: "ready", rows, next: page.next_cursor || "", lastRead: new Date(), problem: null, more: !!page.next_cursor };
      });
      if (mode === "more") walked.current = true;
    } catch (e) {
      if (my !== seq.current) return;
      const err = e as ApiError;
      if (err.status === 401) return;
      const message = readFailed(subject, err);
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
    } finally {
      if (my === seq.current && mode === "more") setBusy(false);
    }
  }, [read, base, subject, state]);

  // Every change of the filters or the sort drops back to the first page.
  React.useEffect(() => {
    walked.current = false;
    void load("first");
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps

  React.useEffect(() => {
    if (!pollMs) return;
    const t = setInterval(() => { if (!walked.current && !document.hidden) void load("poll"); }, pollMs);
    return () => clearInterval(t);
  }, [pollMs, load]);

  return {
    state,
    busy,
    reload: () => { walked.current = false; return load("first"); },
    loadMore: () => load("more"),
  };
}
