import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChannelsTab } from "./channels-tab";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ChannelRow, type ChannelTestReport, listChannels, testChannel } from "@/lib/api";
import {
  ALWAYS_ON,
  CHANNELS_FOOT,
  DELIVERED,
  DELIVERY_FAILED,
  DELIVERY_OK,
  NEVER,
  NOTHING_TO_DELIVER,
  NOT_CONFIGURED,
  NO_TARGETS,
  NO_TARGETS_PUSH,
  ON,
  REACHES_CONSOLE,
  REACHES_NOBODY,
  REACHES_SLACK,
  SEE_WHICH,
  SENDING,
  SEND_TEST,
  SUBJECT_CHANNELS,
  TEST_VERB,
  channelsCount,
  reachesPush,
  targetsCount,
  testName,
  testTitle,
} from "@/lib/approval-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listChannels: vi.fn(),
  testChannel: vi.fn(),
}));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

// The three rows eval answers, in the order the server serves them: the
// console that delivers nothing, Slack switched off, and push reaching one
// of two enrolled devices.
const consoleRow: ChannelRow = {
  name: "console",
  configured: true,
  detail: "always available: approvers check it themselves, nothing is delivered",
};
const slackOff: ChannelRow = {
  name: "slack",
  configured: false,
  detail: "not configured (approval.channels.slack)",
};
const slackOn: ChannelRow = {
  name: "slack",
  configured: true,
  detail: "posting to channel #approvals",
  last_delivery: { at: at(-90), ok: false, note: "card posted: slack answered channel_not_found" },
};
const push: ChannelRow = {
  name: "push",
  configured: true,
  detail: "fcm off · webpush on · apns off · relay on · allowed hosts: ntfy.sh, web.push.apple.com",
  devices: 2,
  registrations: 1,
  last_delivery: { at: at(-7200), ok: true, note: "webpush send" },
};

const rows = () => [consoleRow, slackOff, push];
const withSlack = () => [consoleRow, slackOn, push];

const seeDevices = vi.fn();
const mount = () => render(<TooltipProvider><ChannelsTab onSeeDevices={seeDevices} /></TooltipProvider>);
const row = (name: string) => document.querySelector('[data-channel="' + name + '"]') as HTMLElement;
const status = (name: string) => row(name).querySelector("[data-channel-status]") as HTMLElement;
const panel = () => document.querySelector("[data-channel-test]") as HTMLElement;
const button = (name: string) => screen.getByRole("button", { name: testName(name) });

describe("the channels tab", () => {
  beforeEach(() => {
    vi.mocked(listChannels).mockResolvedValue({ channels: rows() });
    vi.mocked(testChannel).mockResolvedValue({ channel: "push", targets: [] });
    seeDevices.mockClear();
  });
  afterEach(() => { vi.useRealTimers(); });

  it("reads the three rows with their status word, its tone and the server's own detail", async () => {
    mount();
    await waitFor(() => expect(row("console")).toBeTruthy());
    expect(status("console").getAttribute("data-channel-status")).toBe(ALWAYS_ON);
    expect(status("console").className).toContain("text-ok");
    expect(status("slack").getAttribute("data-channel-status")).toBe(NOT_CONFIGURED);
    expect(status("slack").className).not.toContain("text-ok");
    expect(status("push").getAttribute("data-channel-status")).toBe(ON);
    expect(status("push").className).toContain("text-ok");
    // The detail is the server's sentence, rendered exactly as it came.
    expect(within(row("push")).getByText(push.detail as string)).toBeTruthy();
    expect(within(row("slack")).getByText(slackOff.detail as string)).toBeTruthy();
    expect((document.querySelector("[data-row-count]") as HTMLElement).textContent).toBe(channelsCount(3));
  });

  it("says who each channel reaches, and sends the push row to the devices tab", async () => {
    const view = mount();
    await waitFor(() => expect(row("console")).toBeTruthy());
    expect(within(row("console")).getByText(REACHES_CONSOLE)).toBeTruthy();
    expect(within(row("slack")).getByText(REACHES_NOBODY)).toBeTruthy();
    expect(within(row("push")).getByText(reachesPush(2, 1))).toBeTruthy();
    await userEvent.click(within(row("push")).getByRole("button", { name: SEE_WHICH }));
    expect(seeDevices).toHaveBeenCalledTimes(1);
    view.unmount();

    vi.mocked(listChannels).mockResolvedValue({ channels: withSlack() });
    mount();
    await waitFor(() => expect(row("slack")).toBeTruthy());
    expect(within(row("slack")).getByText(REACHES_SLACK)).toBeTruthy();
  });

  it("reads the last delivery as a word, a time and the server's note", async () => {
    const view = mount();
    await waitFor(() => expect(row("console")).toBeTruthy());
    // The console is read by the approver, so it has nothing to deliver and
    // never reads as a channel that has not worked yet.
    expect(within(row("console")).getByText(NOTHING_TO_DELIVER)).toBeTruthy();
    expect(within(row("slack")).getByText(NEVER)).toBeTruthy();
    const ok = within(row("push")).getByText(DELIVERY_OK);
    expect(ok.className).toContain("text-ok");
    expect(within(row("push")).getByText("2 h ago")).toBeTruthy();
    expect(within(row("push")).getByText("webpush send")).toBeTruthy();
    view.unmount();

    vi.mocked(listChannels).mockResolvedValue({ channels: withSlack() });
    mount();
    await waitFor(() => expect(row("slack")).toBeTruthy());
    const failed = within(row("slack")).getByText(DELIVERY_FAILED);
    expect(failed.className).toContain("text-danger");
    expect(within(row("slack")).getByText("card posted: slack answered channel_not_found")).toBeTruthy();
  });

  it("offers Send a test only where there is something to send", async () => {
    const view = mount();
    await waitFor(() => expect(row("console")).toBeTruthy());
    expect(within(row("push")).getByRole("button", { name: testName("push") })).toBeTruthy();
    expect(within(row("console")).queryByRole("button")).toBe(null);
    expect(within(row("slack")).queryByRole("button")).toBe(null);
    view.unmount();

    vi.mocked(listChannels).mockResolvedValue({ channels: withSlack() });
    mount();
    await waitFor(() => expect(row("slack")).toBeTruthy());
    expect(within(row("slack")).getByRole("button", { name: testName("slack") })).toBeTruthy();
  });

  it("sends the test on the row's own channel, reads every target, and reads the list again", async () => {
    vi.mocked(testChannel).mockResolvedValue({
      channel: "push",
      targets: [
        { target: "webpush · device 0199c1a2-7e3f-7000-8000-000000000001 · keyed", ok: true },
        { target: "unifiedpush · device 0199c1a2-7e3f-7000-8000-000000000002 · legacy", ok: false, error: "ntfy.sh answered 502" },
      ],
    });
    mount();
    await waitFor(() => expect(row("push")).toBeTruthy());
    // The next read answers a fresh delivery, so the column has to move.
    vi.mocked(listChannels).mockResolvedValue({
      channels: [consoleRow, slackOff, { ...push, last_delivery: { at: at(0), ok: true, note: "webpush send" } }],
    });
    await userEvent.click(button("push"));
    await waitFor(() => expect(panel()).toBeTruthy());
    expect(testChannel).toHaveBeenCalledWith("push");
    expect(panel().getAttribute("data-channel-test")).toBe("push");
    expect(within(panel()).getByText(testTitle("push"))).toBeTruthy();
    expect(within(panel()).getByText(targetsCount(2))).toBeTruthy();
    expect(within(panel()).getByText(DELIVERED).className).toContain("text-ok");
    expect(within(panel()).getByText(DELIVERY_FAILED).className).toContain("text-danger");
    expect(within(panel()).getByText("webpush · device 0199c1a2-7e3f-7000-8000-000000000001 · keyed")).toBeTruthy();
    expect(within(panel()).getByText("ntfy.sh answered 502")).toBeTruthy();
    await waitFor(() => expect(listChannels).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(within(row("push")).getByText("just now")).toBeTruthy());
  });

  it("reads Sending on the button while its own test runs", async () => {
    let settle: (report: ChannelTestReport) => void = () => {};
    vi.mocked(testChannel).mockReturnValue(new Promise<ChannelTestReport>((res) => { settle = res; }));
    mount();
    await waitFor(() => expect(row("push")).toBeTruthy());
    await userEvent.click(button("push"));
    expect(button("push").textContent).toContain(SENDING);
    expect(button("push").getAttribute("aria-busy")).toBe("true");
    await act(async () => { settle({ channel: "push", targets: [] }); });
    await waitFor(() => expect(button("push").textContent).toContain(SEND_TEST));
    expect(button("push").getAttribute("aria-busy")).toBe(null);
  });

  const empties: [string, ChannelRow[], string][] = [
    ["push", rows(), NO_TARGETS_PUSH],
    ["slack", withSlack(), NO_TARGETS],
  ];
  it.each(empties)("says nothing on %s can receive a request when the test found no target", async (name, served, sentence) => {
    vi.mocked(listChannels).mockResolvedValue({ channels: served });
    vi.mocked(testChannel).mockResolvedValue({ channel: name, targets: [] });
    mount();
    await waitFor(() => expect(row(name)).toBeTruthy());
    await userEvent.click(button(name));
    await waitFor(() => expect(panel()).toBeTruthy());
    expect(within(panel()).getByText(sentence)).toBeTruthy();
    expect(panel().textContent).not.toContain(targetsCount(0));
  });

  const refusals: [string, ApiError, string][] = [
    ["quotes what the server refused", new ApiError("approval: channel is not configured", 400), "The server refused it: approval: channel is not configured. Fix what it names, then try again."],
    ["keeps the outcome uncertain when the response is lost", new ApiError("unreachable", 0, true), "The server did not respond. The change may have been saved. Reload to check before trying again."],
  ];
  it.each(refusals)("%s when the test is refused, and keeps the button", async (_name, err, sentence) => {
    vi.mocked(listChannels).mockResolvedValue({ channels: withSlack() });
    vi.mocked(testChannel).mockRejectedValue(err);
    mount();
    await waitFor(() => expect(row("slack")).toBeTruthy());
    await userEvent.click(button("slack"));
    const block = await screen.findByRole("alert");
    expect(block.textContent).toContain(TEST_VERB + " refused.");
    expect(block.textContent).toContain(sentence);
    expect(block.textContent).not.toContain("state unknown");
    const again = button("slack");
    expect(again.textContent).toContain(SEND_TEST);
    expect(again.hasAttribute("disabled")).toBe(false);
  });

  it("keeps the rows on screen when a later read fails, and stands alone when the first one does", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    vi.mocked(listChannels)
      .mockResolvedValueOnce({ channels: rows() })
      .mockRejectedValue(new ApiError("unreachable", 0, true));
    const view = mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(row("push")).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
    const block = screen.getByRole("status");
    expect(block.textContent).toContain(SUBJECT_CHANNELS + ": unreachable, state unknown.");
    expect(block.textContent).toContain("Check that it is running, then reload.");
    expect(row("push")).toBeTruthy();
    view.unmount();

    vi.mocked(listChannels).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByRole("status").textContent).toContain(SUBJECT_CHANNELS);
    expect(document.querySelector("[data-channel]")).toBe(null);
  });

  it("says where the choice of channels is made, under the table", async () => {
    mount();
    await waitFor(() => expect(row("console")).toBeTruthy());
    const foot = document.querySelector("[data-channels-foot]") as HTMLElement;
    expect(foot.textContent).toBe(CHANNELS_FOOT);
  });
});
