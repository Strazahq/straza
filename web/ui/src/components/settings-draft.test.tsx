import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { DraftSheet } from "./settings-draft";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ConfigAnswer } from "@/lib/api";
import {
  ADD_ROW,
  CLOSE,
  COPY_FRAGMENT,
  DRAFT_EMPTY,
  DRAFT_LEDE,
  DRAFT_TITLE,
  ENV_LABEL,
  FILE_LABEL,
  FRAGMENT_COPIED,
  MISSING_VALUE,
  VALUES_LABEL,
  configRows,
  consequenceOf,
  includeLabel,
  valueLabel,
} from "@/lib/config-words";
import { notify } from "@/lib/notify";

const answer: ConfigAnswer = {
  profile: "enterprise",
  public_url: "http://localhost:8420",
  tls: false,
  store_driver: "postgres",
  events: { embedded: true },
  oidc: { external_issuer: "", jit_provision: false },
  governance: { min_attestation: "none", offline_grace_ttl_seconds: 300, local_tool_default: "deny", audit_backpressure: "block" },
  approval: { gateway_hold_seconds: 120 },
  apps: { gitops_dir_enabled: true, upstream_timeout_seconds: 30 },
  capture: { policy_sets: 1, mode: "verbatim", retention_hours: 720, body_store: "inline" },
};

vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const rows = configRows(answer);
const rowOf = (id: string) => rows.find((r) => r.id === id) as (typeof rows)[number];
const onClose = vi.fn();
const mount = () => render(<TooltipProvider><DraftSheet open rows={rows} onClose={onClose} /></TooltipProvider>);
const fragment = (label: string) => (document.querySelector('[data-fragment="' + label + '"]') as HTMLElement).textContent;

// add picks a row through the Add a row picker.
const add = async (name: string) => {
  await userEvent.click(screen.getByRole("combobox", { name: ADD_ROW }));
  await userEvent.click(await screen.findByRole("option", { name }));
};

// pickValue chooses a value for a row that has a closed set.
const pickValue = async (row: string, value: string) => {
  await userEvent.click(screen.getByRole("combobox", { name: valueLabel(row) }));
  await userEvent.click(await screen.findByRole("option", { name: value }));
};

describe("the Write a config change sheet", () => {
  beforeEach(() => {
    onClose.mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(notify.failed).mockClear();
  });

  it("opens on the lede with nothing picked", async () => {
    mount();
    expect(await screen.findByText(DRAFT_TITLE)).toBeTruthy();
    expect(screen.getByText(DRAFT_LEDE)).toBeTruthy();
    expect(screen.getAllByText(DRAFT_EMPTY).length).toBe(2);
    expect(document.querySelector("[data-fragment]")).toBeNull();
  });

  it("builds the config file, the chart value and the environment from the picked rows", async () => {
    mount();
    await screen.findByText(DRAFT_TITLE);
    await add(rowOf("floor").name);
    await pickValue(rowOf("floor").name, "managed");
    await add(rowOf("grace").name);
    await userEvent.type(screen.getByRole("textbox", { name: valueLabel(rowOf("grace").name) }), "0s");

    expect(fragment(FILE_LABEL)).toBe("governance:\n  minAttestation: \"managed\"\n  offlineGraceTTL: \"0s\"");
    expect(fragment(VALUES_LABEL)).toBe("configYaml: |\n  governance:\n    minAttestation: \"managed\"\n    offlineGraceTTL: \"0s\"");
    expect(fragment(ENV_LABEL)).toBe("STRAZA_MIN_ATTESTATION=managed\n# governance.offlineGraceTTL has no environment variable: set it in the file or the chart");

    // Each picked row keeps its current value beside the new one and says
    // what the change costs.
    expect((document.querySelector('[data-draft-why="floor"]') as HTMLElement).textContent).toBe(consequenceOf(rowOf("floor")));
    expect((document.querySelector('[data-draft-why="grace"]') as HTMLElement).textContent).toBe(consequenceOf(rowOf("grace")));
    expect((document.querySelector('[data-draft-row="grace"]') as HTMLElement).textContent).toContain("5 min");
  });

  it("leaves an unticked row out of the fragment", async () => {
    mount();
    await screen.findByText(DRAFT_TITLE);
    await add(rowOf("floor").name);
    await pickValue(rowOf("floor").name, "managed");
    await add(rowOf("local").name);
    await pickValue(rowOf("local").name, "allow");
    expect(fragment(FILE_LABEL)).toBe("governance:\n  minAttestation: \"managed\"\n  localToolDefault: \"allow\"");

    await userEvent.click(screen.getByRole("checkbox", { name: includeLabel(rowOf("local").name) }));
    expect(fragment(FILE_LABEL)).toBe("governance:\n  minAttestation: \"managed\"");
    expect(fragment(ENV_LABEL)).toBe("STRAZA_MIN_ATTESTATION=managed");
  });

  it("copies the config file block and says so", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await screen.findByText(DRAFT_TITLE);
    await add(rowOf("floor").name);
    await pickValue(rowOf("floor").name, "managed");
    await userEvent.click(screen.getByRole("button", { name: COPY_FRAGMENT }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("governance:\n  minAttestation: \"managed\""));
    expect(notify.ok).toHaveBeenCalledWith(FRAGMENT_COPIED);
  });

  it("keeps the primary clickable and names the field a value is missing from", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await screen.findByText(DRAFT_TITLE);
    await add(rowOf("grace").name);
    const copy = screen.getByRole("button", { name: COPY_FRAGMENT });
    expect(copy.getAttribute("disabled")).toBeNull();
    await userEvent.click(copy);
    expect(screen.getByText(MISSING_VALUE)).toBeTruthy();
    const field = screen.getByRole("textbox", { name: valueLabel(rowOf("grace").name) });
    expect(document.activeElement).toBe(field);
    expect(writeText).not.toHaveBeenCalled();

    await userEvent.type(field, "0s");
    expect(screen.queryByText(MISSING_VALUE)).toBeNull();
    await userEvent.click(copy);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("governance:\n  offlineGraceTTL: \"0s\""));
  });

  it("closes from the footer", async () => {
    mount();
    await screen.findByText(DRAFT_TITLE);
    // The corner X carries the same word, so the footer's own button is
    // the one this asserts.
    const footer = document.querySelector('[data-slot="sheet-footer"]') as HTMLElement;
    await userEvent.click(within(footer).getByRole("button", { name: CLOSE }));
    expect(onClose).toHaveBeenCalled();
  });

  // Two added rows and two typed paths take about half of vitest's 5 s default
  // alone, so the case carries its own budget for a loaded full suite.
  it("drafts both TLS paths as literal strings without exporting the redacted on/off display", async () => {
    mount();
    await screen.findByText(DRAFT_TITLE);
    await add("TLS certificate file");
    await userEvent.type(screen.getByRole("textbox", { name: valueLabel("TLS certificate file") }), "/etc/straza/cert.pem");
    await add("TLS private key file");
    await userEvent.type(screen.getByRole("textbox", { name: valueLabel("TLS private key file") }), "/etc/straza/private key.pem");
    expect(parse(fragment(FILE_LABEL) || "")).toEqual({ server: { tls: { certFile: "/etc/straza/cert.pem", keyFile: "/etc/straza/private key.pem" } } });
    expect(fragment(ENV_LABEL)).toContain("STRAZA_TLS_KEY_FILE='/etc/straza/private key.pem'");
  }, 15000);
});
