import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionRecording, recordedMode } from "./session-recording";
import type { Transcript } from "@/lib/api";
import { navigate } from "@/lib/router";
import type { Read } from "@/lib/user-read";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const ID = "01a1da3d-934c-7be8-bd1a-92e51c045044";
const ready = (modes: string[]): Read<Transcript> => ({ kind: "ready", data: { session_id: ID, turns: modes.map((mode) => ({ mode })) } as Transcript });
const card = () => (document.querySelector("[data-recording-card]") as HTMLElement).textContent;

describe("the Recording card", () => {
  it("says how the turns are recorded and opens the transcript", async () => {
    render(<SessionRecording id={ID} allowed read={ready(["verbatim", "verbatim"])} />);
    expect(card()).toBe("RecordingRecorded, word for word2 turns so far.Open transcript");
    await userEvent.click(screen.getByRole("button", { name: "Open transcript" }));
    expect(navigate).toHaveBeenCalledWith("transcripts", [ID]);
  });

  it.each([
    [["redact"], "Recorded, secrets masked1 turn so far."],
    [["verbatim", "redact"], "Recorded, partly masked2 turns so far."],
  ])("words the turns' modes %j", (modes, words) => {
    render(<SessionRecording id={ID} allowed read={ready(modes)} />);
    expect(card()).toBe("Recording" + words + "Open transcript");
  });

  it("says no policy records the session", () => {
    render(<SessionRecording id={ID} allowed read={ready([])} />);
    expect(card()).toBe("RecordingNot recordedNo policy records this session.");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("says a failed read", () => {
    render(<SessionRecording id={ID} allowed read={{ kind: "failed", message: "The recording could not be read: boom. Reload to try again." }} />);
    expect(card()).toBe("RecordingThe recording could not be read: boom. Reload to try again.");
  });

  it("says in one line that the role does not include Transcripts", () => {
    render(<SessionRecording id={ID} allowed={false} read={{ kind: "off" }} />);
    expect(card()).toBe("RecordingThe recording is not shown, because your role does not include Transcripts.");
  });

  it("says in one muted line that it reads the recording", () => {
    render(<SessionRecording id={ID} allowed read={{ kind: "loading" }} />);
    expect(card()).toBe("RecordingReading the recording.");
    const line = screen.getByRole("status");
    expect(line.textContent).toBe("Reading the recording.");
    expect(line.className).toContain("text-muted-foreground");
  });
});

describe("recordedMode", () => {
  it.each([
    [["verbatim"], "verbatim"],
    [["redact", "redact"], "redact"],
    [["verbatim", "redact"], "mixed"],
    [["something-else"], "redact"],
  ])("reads %j as %s", (modes, want) => {
    expect(recordedMode(modes.map((mode) => ({ mode })))).toBe(want);
  });
});
