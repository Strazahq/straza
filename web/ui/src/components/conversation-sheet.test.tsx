import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConversationSheet } from "./conversation-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type Transcript, sessionTranscript } from "@/lib/api";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), sessionTranscript: vi.fn() }));

const SESSION = "0199cf12-4b1e-7a3d-9f21-8c0b1e2d3a44";
const SHORT = "0199cf12-4b1e…";
const HASH = "sha256:77d0e1c9a4b2f83e5d1607b9c2a4e6f80d3b5172a9c4e6f8091a2b3c4d5e6f70";

// One captured conversation of six turns, the shape
// /v1/admin/sessions/{id}/transcript answers: two exchanges, a reply whose
// body the store lost, a truncated reply, and a prompt carrying markup.
const transcript: Transcript = {
  session_id: SESSION,
  username: "joe-java-developer-agent",
  turns: [
    { at: "2026-09-12T10:20:04Z", kind: "prompt", mode: "redact", content: "Deploy the billing service to staging and tail its logs until the health check passes.", content_hash: "sha256:aa01" },
    { at: "2026-09-12T10:20:09Z", kind: "reply", mode: "redact", content: "I will run the deploy from deploy/helm and follow the pod logs.", content_hash: "sha256:aa02" },
    { at: "2026-09-12T10:31:12Z", kind: "prompt", mode: "redact", content: "Use this key for the registry: [REDACTED credential]", content_hash: "sha256:aa03" },
    { at: "2026-09-12T10:31:15Z", kind: "reply", mode: "redact", content: "The first part of a long answer.", truncated: true, content_hash: "sha256:aa04" },
    { at: "2026-09-12T10:44:40Z", kind: "reply", mode: "redact", content: "", body_missing: true, content_hash: HASH, agent_type: "subagent" },
    { at: "2026-09-12T10:45:00Z", kind: "prompt", mode: "redact", content: "<script>alert(1)</script>", content_hash: "sha256:aa06" },
  ],
};

const mount = (props: Partial<React.ComponentProps<typeof ConversationSheet>> = {}) =>
  render(
    <TooltipProvider>
      <ConversationSheet sessionID={SESSION} open onOpenChange={vi.fn()} {...props} />
    </TooltipProvider>,
  );

// turns returns each rendered turn as its kind and its text, and bands the
// day bands in chat order.
const bands = () => [...document.querySelectorAll("[data-day]")].map((b) => b.textContent);
const turns = () => [...document.querySelectorAll("[data-turn]")].map((t) => [t.getAttribute("data-turn"), t.textContent || ""] as const);

describe("the conversation sheet", () => {
  beforeEach(() => {
    vi.mocked(sessionTranscript).mockResolvedValue(transcript);
  });

  it("reads the session as a chat, prompts right and replies left, with the speaker and the clock", async () => {
    mount();
    await screen.findByText(/Deploy the billing service/);
    expect(sessionTranscript).toHaveBeenCalledWith(SESSION);
    const boxes = [...document.querySelectorAll("[data-turn]")];
    expect(boxes.map((b) => b.getAttribute("data-turn"))).toEqual(["prompt", "reply", "prompt", "reply", "reply", "prompt"]);
    expect(boxes[0].className).toContain("self-end");
    expect(boxes[1].className).toContain("self-start");
    // The speaker of a prompt is the user, of a reply the assistant, and a
    // subagent reply says which agent answered.
    expect(boxes[0].textContent).toContain("joe-java-developer-agent");
    expect(boxes[0].textContent).toContain("10:20:04");
    expect(boxes[1].textContent).toContain("assistant");
    expect(boxes[4].textContent).toContain("assistant · subagent");
    expect(screen.getByRole("dialog").getAttribute("data-conversation-sheet")).toBe(SESSION);
  });

  // Green and the accent are kept for trust states and controls, so no turn
  // carries a coloured rule: the side and the fill say who spoke.
  it.each([
    ["prompt", "self-end", "bg-secondary"],
    ["reply", "self-start", "bg-background"],
  ])("tells a %s apart by its side and its fill, at body size and with no coloured rule", async (kind, side, fill) => {
    mount();
    await screen.findByText(/Deploy the billing service/);
    const boxes = [...document.querySelectorAll("[data-turn='" + kind + "']")];
    expect(boxes).toHaveLength(3);
    for (const box of boxes) {
      for (const cls of [side, fill, "border-border", "text-base"]) expect(box.className).toContain(cls);
      expect(box.className).not.toMatch(/shadow|--ok|--accent/);
    }
  });

  it("names the session, the capture mode once, and the span of the turns", async () => {
    mount();
    await screen.findByText(/Deploy the billing service/);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Session")).toBeTruthy();
    expect(within(dialog).getByText(SHORT)).toBeTruthy();
    const mode = within(dialog).getAllByText("recorded with secrets masked");
    expect(mode).toHaveLength(1);
    expect(mode[0].getAttribute("data-tone")).toBe("ok");
    expect(within(dialog).getByText("joe-java-developer-agent. 6 turns on 2026-09-12 from 10:20:04 to 10:45:00.")).toBeTruthy();
    expect(bands()).toEqual(["2026-09-12"]);
  });

  it("bands each day above its first turn and names both stamps when the conversation crosses midnight", async () => {
    vi.mocked(sessionTranscript).mockResolvedValue({
      ...transcript,
      turns: [
        { at: "2026-09-11T23:50:01Z", kind: "prompt", mode: "redact", content: "Deploy the billing service.", content_hash: "sha256:aa01" },
        { at: "2026-09-11T23:50:09Z", kind: "reply", mode: "redact", content: "Deploying.", content_hash: "sha256:aa02" },
        { at: "2026-09-12T00:03:00Z", kind: "reply", mode: "redact", content: "The health check passes.", content_hash: "sha256:aa03" },
      ],
    });
    mount();
    await screen.findByText("Deploying.");
    expect(bands()).toEqual(["2026-09-11", "2026-09-12"]);
    // The band sits inside the chat, right before the first turn of its day.
    const chat = [...(document.querySelector("[data-chat]") as HTMLElement).children].map((c) => c.getAttribute("data-day") || c.getAttribute("data-turn"));
    expect(chat).toEqual(["2026-09-11", "prompt", "reply", "2026-09-12", "reply"]);
    expect(within(screen.getByRole("dialog")).getByText("joe-java-developer-agent. 3 turns from 2026-09-11 23:50:01 to 2026-09-12 00:03:00.")).toBeTruthy();
  });

  it("exports the conversation as plain text and as JSONL from the footer", async () => {
    let blob: Blob | null = null;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: (b: Blob) => { blob = b; return "#"; } });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => {} });
    mount();
    await screen.findByText(/Deploy the billing service/);
    await userEvent.click(screen.getByRole("button", { name: "Export" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Plain text" }));
    await waitFor(() => expect(blob).not.toBeNull());
    const lines = (await (blob as unknown as Blob).text()).split("\n");
    expect(lines.slice(0, 6)).toEqual([
      "Session " + SESSION,
      "User: joe-java-developer-agent",
      "Recorded with secrets masked. 6 turns from 2026-09-12 10:20:04 UTC to 2026-09-12 10:45:00 UTC.",
      "",
      "[2026-09-12 10:20:04 UTC] joe-java-developer-agent:",
      "Deploy the billing service to staging and tail its logs until the health check passes.",
    ]);
    blob = null;
    await userEvent.click(screen.getByRole("button", { name: "Export" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "JSONL" }));
    await waitFor(() => expect(blob).not.toBeNull());
    const rows = (await (blob as unknown as Blob).text()).trim().split("\n").map((l) => JSON.parse(l));
    expect(rows).toHaveLength(6);
    expect(rows[0]).toMatchObject({ session_id: SESSION, username: "joe-java-developer-agent", at: "2026-09-12T10:20:04Z", kind: "prompt" });
    expect(rows[4]).toMatchObject({ body_missing: true, content_hash: HASH });
  });

  it("keeps Export closed while the turns are not on hand", async () => {
    vi.mocked(sessionTranscript).mockResolvedValue({ session_id: SESSION, username: "joe-java-developer-agent", turns: [] });
    mount();
    await screen.findByText(/No recorded turns/);
    expect((screen.getByRole("button", { name: "Export" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("marks a verbatim conversation in the accent tone", async () => {
    vi.mocked(sessionTranscript).mockResolvedValue({ ...transcript, turns: transcript.turns.map((t) => ({ ...t, mode: "verbatim" })) });
    mount();
    const mode = await screen.findByText("recorded word for word");
    expect(mode.getAttribute("data-tone")).toBe("accent");
  });

  it("says a lost body out loud with its hash, and marks a truncated turn", async () => {
    mount();
    await screen.findByText("(body unavailable)");
    const missing = document.querySelector("[data-body-missing]") as HTMLElement;
    expect(missing.textContent).toBe(
      "The body of this turn should be in the body store and was not found. This is an integrity finding; the hash witness on the audit chain still stands. Hash " + HASH,
    );
    expect(missing.className).toContain("text-danger");
    expect(screen.getByText("Truncated. Full content hash sha256:aa04")).toBeTruthy();
  });

  it("renders a prompt that carries markup as text", async () => {
    mount();
    await screen.findByText("<script>alert(1)</script>");
    expect(document.querySelector("[data-chat] script")).toBeNull();
    expect(turns().some(([, body]) => body.includes("<script>alert(1)</script>"))).toBe(true);
  });

  it("says so when the session captured no turns", async () => {
    vi.mocked(sessionTranscript).mockResolvedValue({ session_id: SESSION, username: "joe-java-developer-agent", turns: [] });
    mount();
    expect(await screen.findByText("No recorded turns: recording is off for this session's policy, or nothing was said.")).toBeTruthy();
    expect(turns()).toEqual([]);
    expect(screen.queryByText("recorded with secrets masked")).toBeNull();
  });

  it("keeps a failed transcript read inside the sheet", async () => {
    vi.mocked(sessionTranscript).mockRejectedValue(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("Conversation: unreachable, state unknown.");
    expect(block.textContent).toContain("The recorded turns of this session could not be read because strazad did not answer. Check that it is running, then reload.");
    expect(screen.getByText("The recorded turns could not be read.")).toBeTruthy();
  });

  it("opens the session and the audit trail from the footer, and closes", async () => {
    const onOpenSession = vi.fn();
    const onOpenAudit = vi.fn();
    const onOpenChange = vi.fn();
    mount({ onOpenSession, onOpenAudit, onOpenChange });
    await screen.findByText(/Deploy the billing service/);
    const footer = document.querySelector("[data-slot=sheet-footer]") as HTMLElement;
    expect(footer.textContent).toContain("Session: open · Audit trail: open");
    await userEvent.click(within(footer).getByRole("button", { name: "Open this session" }));
    expect(onOpenSession).toHaveBeenCalledWith(SESSION);
    await userEvent.click(within(footer).getByRole("button", { name: "Open the audit trail of this session" }));
    expect(onOpenAudit).toHaveBeenCalledWith(SESSION);
    await userEvent.click(within(footer).getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("leaves the footer without doors when no screen answers them", async () => {
    mount();
    await screen.findByText(/Deploy the billing service/);
    const footer = document.querySelector("[data-slot=sheet-footer]") as HTMLElement;
    expect(footer.textContent?.trim()).toBe("ExportClose");
  });
});
