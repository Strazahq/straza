import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EnrollSheet } from "./approver-enroll-sheet";
import { ApiError, type EnrollTokenAnswer, type UserDetail, type UserRow, getUser, listUsers, mintEnrollToken } from "@/lib/api";
import {
  CODE_SPENT,
  COPIED,
  CREATE_ANOTHER,
  CREATE_QR,
  MINT_VERB,
  PERSON_HINT,
  PERSON_LABEL,
  PINNED,
  PLAINTEXT,
  PUBLIC_CA,
  PUBLIC_CA_LINE,
  QR_LABEL,
  SERVERS_NONE,
  TRANSPORT_UNKNOWN,
  TRANSPORT_UNKNOWN_LINE,
  agentPicked,
  codeLeft,
  plaintextLine,
} from "@/lib/approval-words";
import { copyText } from "@/lib/clipboard";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listUsers: vi.fn(),
  getUser: vi.fn(),
  mintEnrollToken: vi.fn(),
}));
vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn() }));

// user is one directory row in the shape the picker reads.
const user = (name: string, extra: Partial<UserRow> = {}): UserRow => ({
  id: "usr_" + name, username: name, display: name, status: "active", origin: "scim",
  kind: "person", created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z",
  effective_roles: [], locks: [], sponsored_count: 0, ...extra,
});
const alice = user("alice");
const joe = user("joe-java-developer-agent", { kind: "nhi", user_type: "agent" });
const detail = (u: UserRow): UserDetail => ({ ...u, counts: { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 } });

const TOKEN = "set_01a0b3f2c9d84e7fa6b1c0d2e3f4a5b6c7d8e9f0";
const PIN = "sha256/VRUrVgq7BM3bUMfYKk+3vSIluBrMOq+Dr8fdS6hNn0Y=";
const payload = (servers: string[], pin?: string) =>
  JSON.stringify({ v: 1, servers, token: TOKEN, ...(pin ? { pin } : {}), project: { id: "eval", name: "Straza eval" } });

// answer is the mint envelope; the pinned https case is what eval answers
// once the approver address is set.
const answer = (extra: Partial<EnrollTokenAnswer> = {}): EnrollTokenAnswer => ({
  enroll_token: TOKEN,
  expires_in: 600,
  user: { id: alice.id, username: "alice" },
  servers: ["https://straza.eval:8443"],
  tls_spki_pin: PIN,
  qr_payload: payload(["https://straza.eval:8443"], PIN),
  ...extra,
});

const mount = (onMinted?: () => void) => render(<EnrollSheet open onOpenChange={() => undefined} onMinted={onMinted} />);
const create = () => screen.getByRole("button", { name: CREATE_QR }) as HTMLButtonElement;
const box = (sel: string) => document.querySelector(sel) as HTMLElement;

// pick types into the picker and takes the row the directory answered.
async function pick(typist: ReturnType<typeof userEvent.setup>, name: string) {
  await typist.type(screen.getByRole("combobox", { name: PERSON_LABEL }), name.slice(0, 3));
  await typist.click(await screen.findByRole("option", { name: new RegExp(name) }));
  await waitFor(() => expect(getUser).toHaveBeenCalled());
}

async function mintFor(typist: ReturnType<typeof userEvent.setup>) {
  await pick(typist, "alice");
  await waitFor(() => expect(create().disabled).toBe(false));
  await typist.click(create());
  await screen.findByRole("img", { name: QR_LABEL });
}

describe("the Add a phone sheet", () => {
  beforeEach(() => {
    vi.mocked(listUsers).mockReset().mockResolvedValue({ items: [alice], next_cursor: "" });
    vi.mocked(getUser).mockReset().mockResolvedValue(detail(alice));
    vi.mocked(mintEnrollToken).mockReset().mockResolvedValue(answer());
    vi.mocked(copyText).mockClear();
  });
  afterEach(() => vi.useRealTimers());

  it("asks for a person before anything can be minted", () => {
    mount();
    expect(screen.getByText(PERSON_HINT)).toBeTruthy();
    expect(create().disabled).toBe(true);
    expect(mintEnrollToken).not.toHaveBeenCalled();
  });

  it("refuses an agent in the console's own words and keeps the button shut", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [joe], next_cursor: "" });
    vi.mocked(getUser).mockResolvedValue(detail(joe));
    const typist = userEvent.setup();
    mount();
    await pick(typist, "joe-java-developer-agent");
    expect(await screen.findByText(agentPicked("joe-java-developer-agent"))).toBeTruthy();
    expect(create().disabled).toBe(true);
  });

  it("opens the button once a person is picked", async () => {
    const typist = userEvent.setup();
    mount();
    await pick(typist, "alice");
    await waitFor(() => expect(create().disabled).toBe(false));
    expect(screen.queryByText(agentPicked("alice"))).toBe(null);
  });

  it("mints for the picked person and draws the server's own payload", async () => {
    const minted = vi.fn();
    const typist = userEvent.setup();
    mount(minted);
    await mintFor(typist);
    expect(mintEnrollToken).toHaveBeenCalledWith(alice.id);
    const svg = screen.getByRole("img", { name: QR_LABEL });
    expect(svg.getAttribute("data-qr-source")).toBe(answer().qr_payload);
    expect(box("[data-enroll-token]").textContent).toBe(TOKEN);
    expect(minted).toHaveBeenCalled();
  });

  it("lists the servers the phone tries, in the order the server gave them", async () => {
    const servers = ["https://straza.eval:8443", "https://10.1.0.9:8443"];
    vi.mocked(mintEnrollToken).mockResolvedValue(answer({ servers, qr_payload: payload(servers, PIN) }));
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    expect([...box("[data-servers]").querySelectorAll("li")].map((li) => li.textContent)).toEqual(servers);
  });

  it("falls back to the enrolment envelope when the server sent no payload", async () => {
    const servers = ["https://straza.eval:8443"];
    vi.mocked(mintEnrollToken).mockResolvedValue(answer({ servers, qr_payload: undefined, tls_spki_pin: undefined }));
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    expect(screen.getByRole("img", { name: QR_LABEL }).getAttribute("data-qr-source"))
      .toBe(JSON.stringify({ v: 1, servers, token: TOKEN }));
  });

  const postures: [string, Partial<EnrollTokenAnswer>, string, string][] = [
    ["a pin from strazad itself", {}, PINNED, PIN],
    ["plain HTTP", { servers: ["http://10.1.0.9:8443"], tls_spki_pin: undefined }, PLAINTEXT, plaintextLine(["http://10.1.0.9:8443"])],
    ["https behind an ingress", { servers: ["https://approve.example.com"], tls_spki_pin: undefined }, PUBLIC_CA, PUBLIC_CA_LINE],
    ["no address at all", { servers: [], tls_spki_pin: undefined }, TRANSPORT_UNKNOWN, TRANSPORT_UNKNOWN_LINE],
  ];
  it.each(postures)("says how the phone will trust %s", async (_case, extra, word, line) => {
    vi.mocked(mintEnrollToken).mockResolvedValue(answer(extra));
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    expect(screen.getByText(word)).toBeTruthy();
    if (word === PINNED) expect(box("[data-pin]").textContent).toBe(line);
    else expect(box("[data-trust-line]").textContent).toBe(line);
    if (word === TRANSPORT_UNKNOWN) expect(screen.getByText(SERVERS_NONE)).toBeTruthy();
  });

  it("quotes a refused mint inside the sheet and keeps the button", async () => {
    const sentence = "strazad found no network address to advertise, so this QR would name https://127.0.0.1:8443, a loopback address a phone cannot dial. Set server.approverTLS.publicUrl (env STRAZA_APPROVER_TLS_PUBLIC_URL) to the https URL the phone should dial, restart strazad, and mint again";
    vi.mocked(mintEnrollToken).mockRejectedValue(new ApiError(sentence, 409));
    const typist = userEvent.setup();
    mount();
    await pick(typist, "alice");
    await waitFor(() => expect(create().disabled).toBe(false));
    await typist.click(create());
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(MINT_VERB + " refused.");
    expect(alert.textContent).toContain(sentence);
    expect(alert.textContent).toContain("Fix what it names, then try again.");
    expect(create()).toBeTruthy();
    expect(screen.queryByRole("img", { name: QR_LABEL })).toBe(null);
  });

  it("copies the one-time code and starts over with the picker cleared", async () => {
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    await typist.click(screen.getByRole("button", { name: /Copy/ }));
    expect(copyText).toHaveBeenCalledWith(TOKEN, COPIED);
    await typist.click(screen.getByRole("button", { name: CREATE_ANOTHER }));
    expect(box("[data-enroll-sheet]").getAttribute("data-enroll-sheet")).toBe("person");
    expect(screen.getByRole("combobox", { name: PERSON_LABEL })).toBeTruthy();
    expect(create().disabled).toBe(true);
  });

  // The clock is read from Date.now(), so only the clock is faked here: the
  // interval stays real and ticks once against the moved time.
  it("counts the code down from the life the server gave it, and says when it expired", async () => {
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    const left = () => box("[data-code-left]").textContent;
    expect(left()).toBe(codeLeft(600));
    const minted = Date.now();
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(minted + 60000);
    await waitFor(() => expect(left()).toBe(codeLeft(540)), { timeout: 3000 });
    vi.setSystemTime(minted + 600000);
    await waitFor(() => expect(left()).toBe(CODE_SPENT), { timeout: 3000 });
  });
});

describe("Add a phone for the signed-in person", () => {
  it("skips the picker, mints through the person's own lane and shows the code", async () => {
    const answer = { enroll_token: "set_self_1", expires_in: 600, user: { id: "u-alice", username: "alice" }, servers: ["https://straza.example:8443"], qr_payload: '{"v":1}' };
    const mint = vi.fn(async () => answer);
    render(<EnrollSheet open onOpenChange={() => {}} self={{ username: "alice", mint }} />);
    expect(document.querySelector("[data-user-picker]")).toBeNull();
    expect(document.querySelector("[data-self-phone]")?.getAttribute("data-self-phone")).toBe("alice");
    await userEvent.click(screen.getByRole("button", { name: "Create the QR code" }));
    await screen.findByText("set_self_1");
    expect(mint).toHaveBeenCalledTimes(1);
  });
});
