import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OriginLine } from "./origin-line";
import { ApiError, type DraftSummary } from "@/lib/api";
import { listDrafts } from "@/lib/drafts-api";
import { navigate } from "@/lib/router";
import { adminAreas } from "@/lib/session";
import { absTime } from "@/lib/words";
import { ALICE, SUMMARY } from "@/test/drafts-fixture";

// The origin line: the file a server came from and
// whether live differs from it, the draft that last published an object,
// and the drafts that wait to change it. Every read fails soft.

vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), listDrafts: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: vi.fn() }));

const FILE = "/etc/straza/apps/demo-tools.yaml";
const DAVE = { ...ALICE, user_id: "u-dave", username: "dave" };
const STRAZAD = { user_id: "", username: "strazad", agent: false, via: "file" as const, client: "strazad" };
const at = (min: number) => "2026-09-24T11:" + String(min).padStart(2, "0") + ":00Z";
const row = (id: string, extra: Partial<DraftSummary>): DraftSummary => ({ ...SUMMARY, id, door: "console", proposer: ALICE, items: [{ kind: "App", name: "demo-tools", op: "put" }], ...extra });
const published = (id: string, min: number, by = ALICE, door: DraftSummary["door"] = "console") => row(id, { state: "published", door, decided_at: at(min), decided_by: by });

// answer serves the published and the open list by the query's state.
function answer(pub: DraftSummary[] | Error, open: DraftSummary[] | Error) {
  vi.mocked(listDrafts).mockImplementation(async (q: string) => {
    const want = q.includes("state=published") ? pub : open;
    if (want instanceof Error) throw want;
    return { items: want, next_cursor: "" };
  });
}
const line = () => document.querySelector("[data-origin]") as HTMLElement | null;
const settle = () => waitFor(() => expect(listDrafts).toHaveBeenCalled());

describe("the origin line", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(adminAreas).mockReturnValue(null);
  });

  it("names the file and the draft that last published a server live matches", async () => {
    answer([published("40", 2)], []);
    render(<OriginLine object="App/demo-tools" name="demo-tools" file={FILE} />);
    await waitFor(() => expect(line()?.textContent).toContain("Last published"));
    expect(line()!.textContent).toBe("Origin: the file demo-tools.yaml in /etc/straza/apps.Last published by alice at " + absTime(at(2)) + ", in draft 40.");
    expect(listDrafts).toHaveBeenCalledWith("state=published&object=App%2Fdemo-tools&limit=20");
    expect(listDrafts).toHaveBeenCalledWith("state=open&object=App%2Fdemo-tools&limit=20");
  });

  it.each([
    { name: "a publish through another door", pub: [published("42", 2, DAVE)], want: "Live differs from the file since dave published draft 42 at " + absTime(at(2)) + "." },
    { name: "the file's own publish", pub: [published("41", 2, ALICE, "apps-directory")], want: "Live differs from the file." },
    { name: "no publish it may read", pub: [], want: "Live differs from the file." },
  ])("says live differs from the file since $name", async ({ pub, want }) => {
    answer(pub, []);
    render(<OriginLine object="App/demo-tools" name="demo-tools" file={FILE} differs />);
    await settle();
    await waitFor(() => expect(line()!.textContent).toBe("Origin: the file demo-tools.yaml in /etc/straza/apps." + want));
  });

  it("points at the file's newest revision when it waits as a draft", async () => {
    answer([published("42", 2, DAVE)], [row("44", { door: "apps-directory", source: FILE, proposer: STRAZAD })]);
    render(<OriginLine object="App/demo-tools" name="demo-tools" file={FILE} differs />);
    await waitFor(() => expect(line()!.textContent).toContain("The file's newest revision waits as draft 44."));
    await userEvent.click(screen.getByRole("button", { name: "Review draft 44" }));
    expect(navigate).toHaveBeenCalledWith("drafts", ["44"]);
    expect(screen.getAllByRole("button")).toHaveLength(1);
  });

  it("names the newest publish by the time it was published, and the drafts that wait", async () => {
    answer([published("45", 1), published("43", 30, DAVE)], [row("48", {}), row("47", {})]);
    render(<OriginLine object="Role/demo-tools-readers" name="demo-tools-readers" />);
    await waitFor(() => expect(line()!.textContent).toContain("2 drafts"));
    expect(line()!.textContent).toBe("Last published by dave at " + absTime(at(30)) + ", in draft 43.2 drafts change demo-tools-readers and wait under Drafts.Review draft 48");
    expect(line()!.querySelector("b")!.textContent).toBe("Last published by dave at " + absTime(at(30)) + ", in draft 43.");
  });

  it("says one waiting draft by its id", async () => {
    answer([], [row("49", {})]);
    render(<OriginLine object="PolicySet/guard" name="guard" />);
    await waitFor(() => expect(line()?.textContent).toBe("Draft 49 changes guard and waits under Drafts.Review draft 49"));
  });

  it("reads no last publish for a reader without drafts:read, who lists only some drafts, and keeps the file", async () => {
    vi.mocked(adminAreas).mockReturnValue({ apps: true });
    answer([published("40", 2)], [row("44", { door: "apps-directory", source: FILE, proposer: STRAZAD }), row("50", {})]);
    render(<OriginLine object="App/demo-tools" name="demo-tools" file={FILE} differs />);
    await waitFor(() => expect(line()!.textContent).toContain("draft 44"));
    expect(line()!.textContent).toBe("Origin: the file demo-tools.yaml in /etc/straza/apps.Live differs from the file.The file's newest revision waits as draft 44.Review draft 44");
    expect(vi.mocked(listDrafts).mock.calls.map((c) => c[0])).toEqual(["state=open&object=App%2Fdemo-tools&limit=20"]);
  });

  it("keeps the file when the reads fail, and shows nothing with no file and nothing read", async () => {
    answer(new ApiError("forbidden", 403), new ApiError("unreachable", 0, true));
    render(<OriginLine object="App/demo-tools" name="demo-tools" file={FILE} />);
    await settle();
    await waitFor(() => expect(line()!.textContent).toBe("Origin: the file demo-tools.yaml in /etc/straza/apps."));
    document.body.innerHTML = "";
    render(<OriginLine object="Role/r" name="r" />);
    await waitFor(() => expect(listDrafts).toHaveBeenCalledTimes(4));
    expect(line()).toBeNull();
  });
});
