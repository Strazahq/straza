import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AccessEditor } from "./access-editor";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type Plan, emptyPlan, grantMatchers, planFor } from "@/lib/access-plan";
import { type AppRow, type BindingRow, type ObjectsAnswer, type ToolRow, appObjects } from "@/lib/api";

// The access editor's On which question on a server that pins repos: two
// cards, the picker that searches the server's list, the tools a row on
// some repos cannot hold, and the row as the role file stores it. A server
// that pins nothing shows none of it.

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), appObjects: vi.fn() }));

const ghapp: AppRow = {
  id: "app-1",
  name: "ghapp",
  runtime: "remote",
  status: "running",
  reached_by: [],
  manifest: {
    straza: {
      objects: {
        type: "repo",
        plural: "repos",
        list: { tool: "repo_list" },
        tools: { pr_create: { argument: "repo" }, pr_view: { argument: "repo" }, get_me: "none", search_code: "credential", repo_create: "all" },
      },
    },
  },
};
const plain: AppRow = { id: "app-2", name: "demo-tools", runtime: "http", status: "running", reached_by: [] };
const NAMES = ["get_me", "pr_create", "pr_view", "repo_create", "search_code"];
const toolsOf = (app: AppRow): ToolRow[] => NAMES.map((n, i) => ({ id: "t" + i, app: app.name, app_id: app.id, name: n }));

const page = (objects: ObjectsAnswer["objects"], total: number, next?: string): ObjectsAnswer => ({ app: "ghapp", type: "repo", plural: "repos", objects, total, next });
const PAY_API = { value: "Strazahq/pay-api", id: "R1", title: "Payments API" };
const PAY_WEB = { value: "Strazahq/pay-web", id: "R2" };
const SITE = { value: "Strazahq/site", id: "R3" };

let latest: Plan = emptyPlan();

function Harness({ app = ghapp, seed, stored = null }: { app?: AppRow; seed?: Plan; stored?: BindingRow | null }) {
  const [plan, setPlan] = React.useState<Plan>(() => seed || planFor(stored, NAMES));
  latest = plan;
  return (
    <TooltipProvider>
      <AccessEditor role="ghapp-write" app={app} tools={toolsOf(app)} plan={plan} onPlan={(u) => setPlan(u)} fixed={{}} approvers={[]} ownSet="ghapp-write-access" stored={stored} />
    </TooltipProvider>
  );
}

const card = (name: string) => screen.getByRole("radio", { name });
// stored opens the fold once and reads the row as the role file stores it.
async function stored() {
  if (!document.querySelector("[data-covers] pre")) await userEvent.click(screen.getByRole("button", { name: "How it is stored" }));
  return (document.querySelector("[data-covers] pre") as HTMLElement).textContent;
}
const sentence = () => (document.querySelector("[data-grant-sentence]") as HTMLElement).textContent;

describe("the On which question", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(appObjects).mockImplementation(async (_id, q) => (q && q.includes("q=site") ? page([SITE], 1) : page([PAY_API, PAY_WEB], 10, "R2")));
  });

  it("is not asked on a server that pins no type", () => {
    render(<Harness app={plain} />);
    expect(document.querySelector("[data-covers]")).toBe(null);
    expect(appObjects).not.toHaveBeenCalled();
  });

  it("offers every repo with today's count and these repos, in the server's words", async () => {
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true } }} />);
    expect(screen.getByText("On which repos")).toBeTruthy();
    expect(screen.getByText("ghapp says 2 of its tools work on one repo. Pick which repos this role reaches with them.")).toBeTruthy();
    await waitFor(() => expect(card("Every repo").textContent).toContain("All 10 today, and every repo ghapp lists later."));
    expect(card("These repos").textContent).toContain("Pick from ghapp's list. A repo added later stays out until you add it.");
    await userEvent.click(card("Every repo"));
    expect(latest.covers).toEqual({ every: true });
    expect(sentence()).toContain("reach 1 of 5 tools of ghapp, on every repo, including the repos ghapp lists later.");
    expect(await stored()).toBe("bindings:\n  - app: ghapp\n    tools:\n      - pr_create\n    covers: every\n");
  });

  it("unticks the tools a row on some repos cannot hold, says so, and greys their rows and the glob", async () => {
    render(<Harness seed={{ ...emptyPlan(), reach: "later" }} />);
    await userEvent.click(card("These repos"));
    expect(grantMatchers(latest, NAMES)).toEqual(["get_me", "pr_create", "pr_view"]);
    expect((document.querySelector("[data-unticked]") as HTMLElement).textContent).toBe("repo_create and search_code were unticked: they cannot be in a role on some repos.");
    expect((document.querySelector('[data-tool="repo_create"] [data-dead-why]') as HTMLElement).textContent).toBe(
      "repo_create cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.",
    );
    expect((document.querySelector('[data-tool="search_code"] [data-dead-why]') as HTMLElement).textContent).toContain("its calls do not name a repo, and Straza does not check a role's own login yet.");
    const box = screen.getByRole("checkbox", { name: "tool repo_create" }) as HTMLInputElement;
    expect(box.disabled).toBe(true);
    const later = card("Every tool, and tools added later");
    expect(later.getAttribute("aria-disabled")).toBe("true");
    expect(later.textContent).toContain("A tool ghapp adds later cannot be in a role on some repos: nothing keeps it to them. Make a role on every repo for it.");
    await userEvent.click(later);
    expect(latest.reach).toBe("tick");
    const today = card("Every tool it has today");
    expect(today.getAttribute("aria-disabled")).toBe("true");
    expect(today.textContent).toContain("repo_create cannot be in a role on some repos");
  });

  it("names only the dead tools in the untick note, never a tool a deny kept out", async () => {
    render(<Harness seed={{ ...emptyPlan(), reach: "later", call: "per", choice: { pr_create: "deny" } }} />);
    await userEvent.click(card("These repos"));
    expect(grantMatchers(latest, NAMES)).toEqual(["get_me", "pr_view"]);
    expect(document.querySelector("[data-unticked]")?.textContent).toBe("repo_create and search_code were unticked: they cannot be in a role on some repos.");
  });

  it("keeps the untick note while repos are picked, and clears it when the row leaves These", async () => {
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true, repo_create: true } }} />);
    await userEvent.click(card("These repos"));
    expect(document.querySelector("[data-unticked]")?.textContent).toBe("repo_create was unticked: it cannot be in a role on some repos.");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-api" }));
    expect(document.querySelector("[data-unticked]")?.textContent).toBe("repo_create was unticked: it cannot be in a role on some repos.");
    await userEvent.click(card("Every repo"));
    expect(document.querySelector("[data-unticked]")).toBe(null);
  });

  // Each case is a listing that has not given a list, and the sentences
  // the picker says in place of an empty list: no count on the Every card.
  const unlisted: [string, ObjectsAnswer["listing"], string][] = [
    ["before the first listing", { tool: "repo_list", credential: "set", outcome: "never", version: 0, count: 0 }, "No listing yet. Straza runs the first one within 30 seconds. Reload the page to see it."],
    ["with no listing credential", { tool: "repo_list", credential: "missing", outcome: "failed", version: 0, count: 0 }, "ghapp has no listing credential, so Straza cannot list its repos."],
  ];
  it.each(unlisted)("says why the list is empty %s, and counts nothing", async (_case, listing, line) => {
    vi.mocked(appObjects).mockResolvedValue({ ...page([], 0), listing });
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true }, covers: { objects: [] } }} />);
    await waitFor(() => expect(document.querySelector("[data-picker] [data-listing]")?.textContent).toContain(line));
    expect(card("Every repo").textContent).toContain("Every repo ghapp lists, today and later.");
    expect(document.querySelector("[data-picker]")?.textContent).not.toContain("The list holds no repos.");
  });

  it("turns the Every card off with R4 when the session may not write it, unless the stored line is every", async () => {
    const held: AppRow = { ...ghapp, may_cover_every: false };
    const { unmount } = render(<Harness app={held} seed={{ ...emptyPlan(), picked: { pr_create: true } }} />);
    const every = card("Every repo");
    expect(every.getAttribute("aria-disabled")).toBe("true");
    expect(every.textContent).toContain("On this deployment only a global admin may make ghapp-write cover every repo, including the repos ghapp lists later. Name the repos it covers, or ask a global admin.");
    await userEvent.click(every);
    expect(latest.covers).toBe(undefined);
    unmount();
    render(<Harness app={held} stored={{ id: "b-1", app: "ghapp", role: "ghapp-write", tools: ["pr_create"], covers: { every: true } }} />);
    expect(card("Every repo").getAttribute("aria-disabled")).toBe(null);
    expect(card("Every repo").getAttribute("aria-checked")).toBe("true");
  });

  it("picks repos from the server's list by search, never by typing a value", async () => {
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true } }} />);
    await userEvent.click(card("These repos"));
    expect(screen.getByText("None picked yet. Tick repos in the list below.")).toBeTruthy();
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-api" }));
    expect(screen.getByText("Payments API")).toBeTruthy();
    await userEvent.type(screen.getByLabelText("Filter repos"), "site");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/site" }));
    expect(latest.covers).toEqual({ objects: [{ value: "Strazahq/pay-api", id: "R1", title: "Payments API" }, { value: "Strazahq/site", id: "R3", title: undefined }] });
    expect(vi.mocked(appObjects).mock.calls.map((c) => c[1])).toContain("q=site");
    expect(screen.getByText("2 repos picked")).toBeTruthy();
    expect(sentence()).toContain("on 2 repos: Strazahq/pay-api and Strazahq/site.");
    expect(await stored()).toBe("bindings:\n  - app: ghapp\n    tools:\n      - pr_create\n    covers:\n      objects:\n        - Strazahq/pay-api\n        - Strazahq/site\n");
    await userEvent.click(screen.getByRole("button", { name: "Remove Strazahq/site" }));
    expect(latest.covers).toEqual({ objects: [{ value: "Strazahq/pay-api", id: "R1", title: "Payments API" }] });
  });

  it("loads the next page of the list after the last one", async () => {
    vi.mocked(appObjects).mockImplementation(async (_id, q) => (q && q.includes("after=R2") ? page([SITE], 10) : page([PAY_API, PAY_WEB], 10, "R2")));
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true }, covers: { objects: [] } }} />);
    await userEvent.click(await screen.findByRole("button", { name: /Load more/ }));
    expect(await screen.findByRole("checkbox", { name: "Pick Strazahq/site" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Load more/ })).toBe(null);
  });

  it("lets a stored row on some repos untick a tool it can no longer hold, and never tick it again", async () => {
    render(<Harness stored={{ id: "b-1", app: "ghapp", role: "ghapp-write", tools: ["pr_create", "repo_create"], covers: { objects: [{ value: "Strazahq/pay-web", id: "R2" }] } }} />);
    const box = screen.getByRole("checkbox", { name: "tool repo_create" }) as HTMLInputElement;
    expect(box.checked).toBe(true);
    await userEvent.click(box);
    expect(grantMatchers(latest, NAMES)).toEqual(["pr_create"]);
    expect(box.disabled).toBe(true);
  });

  it("opens on the stored line, with its repos picked", async () => {
    render(<Harness stored={{ id: "b-1", app: "ghapp", role: "ghapp-write", tools: ["pr_create"], covers: { objects: [{ value: "Strazahq/pay-web", id: "R2" }] } }} />);
    expect(card("These repos").getAttribute("aria-checked")).toBe("true");
    expect((await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-web" }) as HTMLInputElement).checked).toBe(true);
    expect(document.querySelector('[data-pill="Strazahq/pay-web"]')).toBeTruthy();
  });

  it("says the list could not be read and leaves the count out of the Every card", async () => {
    vi.mocked(appObjects).mockRejectedValue(Object.assign(new Error("boom"), { status: 500 }));
    render(<Harness seed={{ ...emptyPlan(), picked: { pr_create: true }, covers: { objects: [] } }} />);
    await waitFor(() => expect(document.querySelector("[data-picker]")?.textContent).toContain("The repos"));
    expect(card("Every repo").textContent).toContain("Every repo ghapp lists, today and later.");
  });
});
