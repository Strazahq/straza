import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EnrollSheet } from "./approver-enroll-sheet";
import { ApiError, type EnrollTokenAnswer, type SelfAnswer, readSelf, selfEnrollToken } from "@/lib/api";
import {
  CODE_SPENT,
  COPIED,
  CREATE_ANOTHER,
  CREATE_QR,
  ENROLL_TEXT_TITLE,
  FOR_OTHERS_LINE,
  FOR_OTHERS_TITLE,
  MINT_VERB,
  NO_ENROLL_ROLE,
  PINNED,
  PLAINTEXT,
  PUBLIC_CA,
  PUBLIC_CA_LINE,
  QR_LABEL,
  QR_TOO_BIG,
  SERVERS_NONE,
  SUBJECT_SELF,
  TRANSPORT_UNKNOWN,
  TRANSPORT_UNKNOWN_LINE,
  codeLeft,
  plaintextLine,
  selfPhoneLine,
} from "@/lib/approval-words";
import { copyText } from "@/lib/clipboard";
import { readFailed } from "@/lib/say";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  readSelf: vi.fn(),
  selfEnrollToken: vi.fn(),
}));
vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn() }));

// alice is the signed-in administrator as GET /v1/self answers her.
const alice: SelfAnswer = { username: "alice", user_kind: "human", admin_grants: "full", enroll_channels: ["browser", "mobile"], sponsored: [] };

const TOKEN = "set_01a0b3f2c9d84e7fa6b1c0d2e3f4a5b6c7d8e9f0";
const PIN = "sha256/VRUrVgq7BM3bUMfYKk+3vSIluBrMOq+Dr8fdS6hNn0Y=";
const payload = (servers: string[], pin?: string) =>
  JSON.stringify({ v: 1, servers, token: TOKEN, ...(pin ? { pin } : {}), project: { id: "eval", name: "Straza eval" } });

// answer is the mint envelope; the pinned https case is what eval answers
// once the approver address is set.
const answer = (extra: Partial<EnrollTokenAnswer> = {}): EnrollTokenAnswer => ({
  enroll_token: TOKEN,
  expires_in: 600,
  user: { id: "usr_alice", username: "alice" },
  servers: ["https://straza.eval:8443"],
  tls_spki_pin: PIN,
  qr_payload: payload(["https://straza.eval:8443"], PIN),
  ...extra,
});

const mount = (onMinted?: () => void) => render(<EnrollSheet open onOpenChange={() => undefined} onMinted={onMinted} />);
const create = () => screen.getByRole("button", { name: CREATE_QR }) as HTMLButtonElement;
const box = (sel: string) => document.querySelector(sel) as HTMLElement;
const SELF_SERVICE = window.location.origin + "/self-service/";

async function mintFor(typist: ReturnType<typeof userEvent.setup>) {
  await waitFor(() => expect(create().disabled).toBe(false));
  await typist.click(create());
  await screen.findByRole("img", { name: QR_LABEL });
}

describe("the console's Add a phone sheet", () => {
  beforeEach(() => {
    vi.mocked(readSelf).mockReset().mockResolvedValue(alice);
    vi.mocked(selfEnrollToken).mockReset().mockResolvedValue(answer());
    vi.mocked(copyText).mockClear();
  });
  afterEach(() => vi.useRealTimers());

  it("is for the signed-in person's own phone and never asks for a person", async () => {
    mount();
    expect(await screen.findByText(selfPhoneLine("alice"))).toBeTruthy();
    expect(readSelf).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("combobox")).toBe(null);
    expect(box("[data-self-phone]").getAttribute("data-self-phone")).toBe("alice");
    expect(create().disabled).toBe(false);
    expect(selfEnrollToken).not.toHaveBeenCalled();
  });

  it("says how someone else gets a phone, with the self-service address to copy", async () => {
    const typist = userEvent.setup();
    mount();
    await screen.findByText(selfPhoneLine("alice"));
    expect(screen.getByText(FOR_OTHERS_TITLE)).toBeTruthy();
    expect(screen.getByText(FOR_OTHERS_LINE)).toBeTruthy();
    expect(box("[data-self-service]").textContent).toBe(SELF_SERVICE);
    await typist.click(screen.getByRole("button", { name: /Copy/ }));
    expect(copyText).toHaveBeenCalledWith(SELF_SERVICE, COPIED);
  });

  it("says the account holds no phone role and keeps the button shut", async () => {
    vi.mocked(readSelf).mockResolvedValue({ ...alice, username: "dana", enroll_channels: ["browser"] });
    mount();
    expect(await screen.findByText(NO_ENROLL_ROLE)).toBeTruthy();
    expect(screen.queryByText(selfPhoneLine("dana"))).toBe(null);
    expect(screen.getByText(FOR_OTHERS_LINE)).toBeTruthy();
    expect(create().disabled).toBe(true);
    expect(selfEnrollToken).not.toHaveBeenCalled();
  });

  it("says what could not be read when the self read fails, and keeps the button shut", async () => {
    const err = new ApiError("unreachable", 0, true);
    vi.mocked(readSelf).mockRejectedValue(err);
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain(readFailed(SUBJECT_SELF, err));
    expect(create().disabled).toBe(true);
  });

  it("mints the person's own phone on the self route and draws the server's own payload", async () => {
    const minted = vi.fn();
    const typist = userEvent.setup();
    mount(minted);
    await mintFor(typist);
    expect(selfEnrollToken).toHaveBeenCalledWith("mobile");
    const svg = screen.getByRole("img", { name: QR_LABEL });
    expect(svg.getAttribute("data-qr-source")).toBe(answer().qr_payload);
    expect(box("[data-enroll-token]").textContent).toBe(TOKEN);
    expect(minted).toHaveBeenCalled();
  });

  it("lists the servers the phone tries, in the order the server gave them", async () => {
    const servers = ["https://straza.eval:8443", "https://10.1.0.9:8443"];
    vi.mocked(selfEnrollToken).mockResolvedValue(answer({ servers, qr_payload: payload(servers, PIN) }));
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    expect([...box("[data-servers]").querySelectorAll("li")].map((li) => li.textContent)).toEqual(servers);
  });

  it("falls back to the enrolment envelope when the server sent no payload", async () => {
    const servers = ["https://straza.eval:8443"];
    vi.mocked(selfEnrollToken).mockResolvedValue(answer({ servers, qr_payload: undefined, tls_spki_pin: undefined }));
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
    vi.mocked(selfEnrollToken).mockResolvedValue(answer(extra));
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
    vi.mocked(selfEnrollToken).mockRejectedValue(new ApiError(sentence, 409));
    const typist = userEvent.setup();
    mount();
    await waitFor(() => expect(create().disabled).toBe(false));
    await typist.click(create());
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(MINT_VERB + " refused.");
    expect(alert.textContent).toContain(sentence);
    expect(alert.textContent).toContain("Fix what it names, then try again.");
    expect(create()).toBeTruthy();
    expect(screen.queryByRole("img", { name: QR_LABEL })).toBe(null);
  });

  it("copies the one-time code and starts over on the first view", async () => {
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    await typist.click(screen.getByRole("button", { name: /Copy/ }));
    expect(copyText).toHaveBeenCalledWith(TOKEN, COPIED);
    await typist.click(screen.getByRole("button", { name: CREATE_ANOTHER }));
    expect(box("[data-enroll-sheet]").getAttribute("data-enroll-sheet")).toBe("person");
    expect(screen.getByText(selfPhoneLine("alice"))).toBeTruthy();
    expect(create().disabled).toBe(false);
  });

  // A long server list makes a text past the largest QR code, and the real
  // encoder refuses it.
  it("hands over the enrollment text when the code is too large to draw", async () => {
    const big = payload(Array.from({ length: 120 }, (_, i) => "https://approver-" + i + ".straza.example.com:8443"), PIN);
    vi.mocked(selfEnrollToken).mockResolvedValue(answer({ qr_payload: big }));
    const typist = userEvent.setup();
    mount();
    await waitFor(() => expect(create().disabled).toBe(false));
    await typist.click(create());
    expect(await screen.findByText(QR_TOO_BIG)).toBeTruthy();
    expect(screen.queryByRole("img", { name: QR_LABEL })).toBe(null);
    expect(screen.getByText(ENROLL_TEXT_TITLE)).toBeTruthy();
    expect(box("[data-enroll-text]").textContent).toBe(big);
    const copies = screen.getAllByRole("button", { name: /Copy/ });
    await typist.click(copies[copies.length - 1]);
    expect(copyText).toHaveBeenCalledWith(big, COPIED);
  });

  it("shows no enrollment text when the code is drawn", async () => {
    const typist = userEvent.setup();
    mount();
    await mintFor(typist);
    expect(document.querySelector("[data-enroll-text]")).toBe(null);
    expect(screen.queryByText(ENROLL_TEXT_TITLE)).toBe(null);
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

describe("Add a phone on the self-service page", () => {
  beforeEach(() => {
    vi.mocked(readSelf).mockReset();
    vi.mocked(selfEnrollToken).mockReset();
  });

  it("mints through the page's own lane, reads nothing itself and says nothing of someone else", async () => {
    const answer = { enroll_token: "set_self_1", expires_in: 600, user: { id: "u-alice", username: "alice" }, servers: ["https://straza.example:8443"], qr_payload: '{"v":1}' };
    const mint = vi.fn(async () => answer);
    render(<EnrollSheet open onOpenChange={() => {}} self={{ username: "alice", mint }} />);
    expect(document.querySelector("[data-self-phone]")?.getAttribute("data-self-phone")).toBe("alice");
    expect(screen.queryByText(FOR_OTHERS_TITLE)).toBe(null);
    expect(readSelf).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Create the QR code" }));
    await screen.findByText("set_self_1");
    expect(mint).toHaveBeenCalledTimes(1);
    expect(selfEnrollToken).not.toHaveBeenCalled();
  });
});
