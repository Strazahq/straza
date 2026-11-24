import { describe, expect, it } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { ApiError, type Page } from "./api";
import { type Sorting, usePagedList } from "./paged";

const BY_USER: Sorting = { key: "user", order: "asc" };
const BY_STATUS: Sorting = { key: "status", order: "asc" };

// server answers each read with the rows of its sort, and holds a read
// while hold is set until go answers it.
function server() {
  const held: (() => void)[] = [];
  const s = {
    hold: false,
    fail: false,
    read: (q: string) => new Promise<Page<string>>((resolve, reject) => {
      const sort = new URLSearchParams(q).get("sort");
      const answer = () => (s.fail ? reject(new ApiError("database unavailable", 503, false)) : resolve({ items: [sort + "-1", sort + "-2"], next_cursor: "" }));
      if (s.hold) held.push(answer); else answer();
    }),
    go: () => act(async () => { held.splice(0).forEach((f) => f()); }),
  };
  return s;
}

const mount = (s: ReturnType<typeof server>) =>
  renderHook(({ sorting }) => usePagedList<string>({ read: s.read, subject: "The list", params: {}, sorting }), { initialProps: { sorting: BY_USER as Sorting | null } });

describe("usePagedList", () => {
  it("keeps the old rows with the sort they were read in until the page of the new sort lands", async () => {
    const s = server();
    const { result, rerender } = mount(s);
    await waitFor(() => expect(result.current.state).toMatchObject({ kind: "ready", rows: ["user-1", "user-2"], sorting: BY_USER }));

    s.hold = true;
    rerender({ sorting: BY_STATUS });
    expect(result.current.state).toMatchObject({ kind: "ready", rows: ["user-1", "user-2"], sorting: BY_USER });

    await s.go();
    expect(result.current.state).toMatchObject({ kind: "ready", rows: ["status-1", "status-2"], sorting: BY_STATUS });
  });

  it("keeps the old rows and their sort through a read of the new sort that fails", async () => {
    const s = server();
    const { result, rerender } = mount(s);
    await waitFor(() => expect(result.current.state).toMatchObject({ kind: "ready", sorting: BY_USER }));

    s.fail = true;
    rerender({ sorting: BY_STATUS });
    await waitFor(() => expect(result.current.state).toMatchObject({ problem: "The list could not be read: database unavailable. Reload to try again." }));
    expect(result.current.state).toMatchObject({ kind: "ready", rows: ["user-1", "user-2"], sorting: BY_USER });
  });

  it("holds no sort for rows read without one", async () => {
    const s = server();
    const { result, rerender } = mount(s);
    await waitFor(() => expect(result.current.state).toMatchObject({ kind: "ready", sorting: BY_USER }));
    rerender({ sorting: null });
    await waitFor(() => expect(result.current.state).toMatchObject({ kind: "ready", rows: ["null-1", "null-2"], sorting: null }));
  });
});
