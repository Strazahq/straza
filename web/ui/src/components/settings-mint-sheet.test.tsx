import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MintSheet } from "./settings-mint-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApiTokenRow, type MintedToken, createApiToken } from "@/lib/api";
import { notify } from "@/lib/notify";
import {
  AREAS_TITLE,
  AREA_COLUMN,
  COPIED,
  COPY_NOW,
  DEFAULT_LIFETIME,
  GRANTS_MISSING,
  GRANTS_PLACEHOLDER,
  LEVEL_WRITE,
  NAME_FREE,
  NAME_MISSING,
  NEVER_WARN,
  ROOT_WARN,
  SHOWN_ONCE,
  areaHint,
  curlLine,
  expiresLine,
  expiresWord,
  headerLine,
  levelHint,
  mintedTitle,
  nameTaken,
  useLine,
} from "@/lib/settings-words";
import { dayOf } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  createApiToken: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const DAY = 86400000;
const at = (ms: number) => new Date(Date.now() + ms).toISOString();
const held: ApiTokenRow[] = [{ id: "tok-mid", name: "midpoint", scope: "scim:read", created: at(-6 * DAY) }];

const onMinted = vi.fn();
const onOpenChange = vi.fn();
const mount = (open = true) =>
  render(
    <TooltipProvider>
      <MintSheet open={open} onOpenChange={onOpenChange} rows={held} onMinted={onMinted} />
    </TooltipProvider>,
  );
const sheet = () => screen.getByRole("dialog");
const scope = () => (sheet().querySelector("[data-scope]") as HTMLElement).textContent;
const name = () => screen.getByLabelText("Name");
const pick = (job: string) => userEvent.click(screen.getByRole("radio", { name: job }));
const segment = (area: string, label: string) =>
  within(screen.getByRole("radiogroup", { name: area + " access" })).getByText(label);

describe("the mint sheet", () => {
  beforeEach(() => {
    vi.mocked(createApiToken).mockResolvedValue({ id: "tok-new", name: "sailpoint", scope: "audit:read", token: "wat_SECRET", expires: at(90 * DAY) });
    vi.mocked(notify.ok).mockClear();
    onMinted.mockClear();
    onOpenChange.mockClear();
  });

  it("opens on the 90 day lifetime with no job picked and no scope to send", () => {
    mount();
    expect(screen.getByRole("combobox", { name: "Lifetime" }).textContent).toBe("expires in 90 days");
    expect(screen.getByText(GRANTS_PLACEHOLDER)).toBeTruthy();
    expect(sheet().querySelector("[data-scope]")).toBe(null);
  });

  it("checks the name against the tokens already on screen, as it is typed", async () => {
    mount();
    await userEvent.type(name(), "midpoint");
    expect(screen.getByText(nameTaken("midpoint"))).toBeTruthy();
    await userEvent.clear(name());
    await userEvent.type(name(), "sailpoint");
    expect(screen.getByText(NAME_FREE)).toBeTruthy();
  });

  it("stays clickable with an empty name and says which field is missing", async () => {
    mount();
    await pick("Audit automation");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    expect(screen.getByText(NAME_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(name());
    expect(createApiToken).not.toHaveBeenCalled();
  });

  it("says the grants are missing when a name is typed and no job is picked", async () => {
    mount();
    await userEvent.type(name(), "sailpoint");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    expect(screen.getByText(GRANTS_MISSING)).toBeTruthy();
    expect(createApiToken).not.toHaveBeenCalled();
  });

  const jobs: [string, string][] = [
    ["IGA connector", "scope: apps:read,changes:read,config:read,identity:read,scim:read,scim:write"],
    ["Audit automation", "scope: audit:read,sessions:read"],
    ["Policy CI pipeline", "scope: policy:read,policy:write"],
    ["Read-only reviewer", "scope: audit:read,sessions:read,transcripts:read"],
    ["Full root", "scope: full"],
  ];
  it.each(jobs)("builds the canonical scope of the %s card", async (job, line) => {
    mount();
    await pick(job);
    expect(scope()).toBe(line);
  });

  it("drops exactly the grant whose cross is clicked", async () => {
    mount();
    await pick("IGA connector");
    expect(within(sheet()).getByText("polls the change feed")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Remove apps:read" }));
    expect(scope()).toBe("scope: changes:read,config:read,identity:read,scim:read,scim:write");
  });

  it("warns that a root credential mints further tokens, by card and by area", async () => {
    mount();
    await pick("Full root");
    expect(screen.getByText(ROOT_WARN)).toBeTruthy();
    // full is everything, so the area table under it would be a half-truth.
    expect(sheet().querySelector("[data-area-table]")).toBe(null);
    await pick("Custom");
    await userEvent.click(segment("tokens", "read and write"));
    expect(scope()).toBe("scope: tokens:read,tokens:write");
    expect(screen.getByText(ROOT_WARN)).toBeTruthy();
  });

  it("prints the meaning of the level picked under the area row", async () => {
    mount();
    await pick("Audit automation");
    await pick("Custom");
    const areas = sheet().querySelector("[data-section='Areas']") as HTMLElement;
    const table = areas.querySelector("[data-area-table]") as HTMLElement;
    expect(segment("audit", "read").getAttribute("aria-checked")).toBe("true");
    expect((table.querySelector("[data-level-hint='audit']") as HTMLElement).textContent).toBe(levelHint("audit", "read"));
    expect(within(areas).getByText(AREAS_TITLE)).toBeTruthy();
    expect(within(table).getByText(AREA_COLUMN.covers)).toBeTruthy();
    expect(within(table).getByText(areaHint("audit"))).toBeTruthy();
    expect(table.querySelector("[data-level-hint='policy']")).toBe(null);
    await userEvent.click(segment("audit", "read and write"));
    expect((table.querySelector("[data-level-hint='audit']") as HTMLElement).textContent).toBe(levelHint("audit", "rw"));
  });

  it("renders a held write-only grant as its own level and never offers it elsewhere", async () => {
    mount();
    await pick("Policy CI pipeline");
    await userEvent.click(screen.getByRole("button", { name: "Remove policy:read" }));
    expect(scope()).toBe("scope: policy:write");
    await pick("Custom");
    const seg = segment("policy", LEVEL_WRITE);
    expect(seg.getAttribute("aria-checked")).toBe("true");
    expect((sheet().querySelector("[data-level-hint='policy']") as HTMLElement).textContent).toBe(levelHint("policy", "write"));
    expect(within(screen.getByRole("radiogroup", { name: "audit access" })).queryByText(LEVEL_WRITE)).toBe(null);
  });

  it("warns when never is picked as the lifetime, and takes the warning back", async () => {
    mount();
    await userEvent.click(screen.getByRole("combobox", { name: "Lifetime" }));
    await userEvent.click(await screen.findByRole("option", { name: "never expires" }));
    expect(screen.getByText(NEVER_WARN)).toBeTruthy();
    await userEvent.click(screen.getByRole("combobox", { name: "Lifetime" }));
    await userEvent.click(await screen.findByRole("option", { name: "expires in 90 days" }));
    expect(screen.queryByText(NEVER_WARN)).toBe(null);
  });

  it("mints with the name, the canonical scope and the lifetime, then shows the secret once", async () => {
    mount();
    await userEvent.type(name(), "sailpoint");
    await pick("Audit automation");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    await waitFor(() => expect(createApiToken).toHaveBeenCalledWith("sailpoint", "audit:read,sessions:read", DEFAULT_LIFETIME));
    expect(onMinted).toHaveBeenCalled();
    expect(await screen.findByText(mintedTitle("sailpoint"))).toBeTruthy();
    expect(within(sheet()).getByText(SHOWN_ONCE)).toBeTruthy();
    expect(within(sheet()).getByText("wat_SECRET")).toBeTruthy();
    expect(within(sheet()).getByText(COPY_NOW)).toBeTruthy();
    expect(within(sheet()).getByText(headerLine("wat_SECRET"))).toBeTruthy();
    expect(within(sheet()).getByText(curlLine(window.location.origin))).toBeTruthy();
    expect(within(sheet()).getByText(useLine(false))).toBeTruthy();
    const expires = at(90 * DAY);
    expect((sheet().querySelector("[data-expires-line]") as HTMLElement).textContent)
      .toBe(expiresLine(expiresWord(expires), dayOf(expires)));
    await userEvent.click(within(sheet()).getByRole("button", { name: "Done" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("copies the secret and says it copied", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    mount();
    await userEvent.type(name(), "sailpoint");
    await pick("Audit automation");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    await userEvent.click(await screen.findByRole("button", { name: "Copy" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("wat_SECRET"));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith(COPIED));
  });

  it("prints the server's own sentence under the name when the mint is refused, with no secret left on screen", async () => {
    const view = mount();
    await userEvent.type(name(), "sailpoint");
    await pick("Audit automation");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    expect(await screen.findByText(SHOWN_ONCE)).toBeTruthy();

    // The next open is a fresh sheet, and this time the server refuses.
    view.rerender(<TooltipProvider><MintSheet open={false} onOpenChange={onOpenChange} rows={held} onMinted={onMinted} /></TooltipProvider>);
    view.rerender(<TooltipProvider><MintSheet open={true} onOpenChange={onOpenChange} rows={held} onMinted={onMinted} /></TooltipProvider>);
    vi.mocked(createApiToken).mockRejectedValue(new ApiError("an API token named midpoint already exists. Revoke it first or pick another name", 409));
    await userEvent.type(name(), "midpoint");
    await pick("IGA connector");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));

    const said = await within(sheet()).findByRole("alert");
    expect(said.textContent).toContain("an API token named midpoint already exists");
    expect(said.textContent).toContain("Fix what it names, then try again.");
    expect(said.textContent).not.toContain("retrying");
    expect(said.textContent).not.toContain("state unknown");
    expect(screen.queryByText(SHOWN_ONCE)).toBe(null);
    expect(sheet().textContent).not.toContain("wat_SECRET");
    // The form is as it was: the name and the job it was mint with.
    expect((name() as HTMLInputElement).value).toBe("midpoint");
    expect(scope()).toBe("scope: apps:read,changes:read,config:read,identity:read,scim:read,scim:write");
  });

  it("reads Minting while the server answers and holds its siblings", async () => {
    let land: (v: MintedToken) => void = () => undefined;
    vi.mocked(createApiToken).mockReturnValue(new Promise<MintedToken>((resolve) => { land = resolve; }));
    mount();
    await userEvent.type(name(), "sailpoint");
    await pick("Audit automation");
    await userEvent.click(screen.getByRole("button", { name: "Mint token" }));
    const minting = await screen.findByRole("button", { name: "Minting…" });
    expect(minting.getAttribute("aria-busy")).toBe("true");
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
    land({ id: "tok-new", name: "sailpoint", scope: "audit:read,sessions:read", token: "wat_SECRET" });
    expect(await screen.findByText(mintedTitle("sailpoint"))).toBeTruthy();
  });
});
