// The state the four tabs share with the shell: the session as the tabs
// may show it, the /v1/self answer, the enrolment record and the storage
// outlook, the waiting requests and the credentials the shell counts, plus
// the shell's doors for a tab to open the sign-in, replace the enrolment,
// count its rows and post a page-level notice.
import * as React from "react";
import type { ApprovalRow, SelfAnswer } from "@/lib/api";
import type { CheckinResponse } from "@/lib/session";
import type { CredentialRow } from "./credential-rows";
import type { SelfTab } from "./router";
import type { Enrollment, StorageOutlook } from "./store";

export type Notice = { tone: "ok" | "warn" | "danger" | "unknown"; text: string };

export type SessionInfo =
  | { kind: "booting" }
  | { kind: "signed-out" }
  | { kind: "signed-in"; user: string; grants: string; servers: number };

export type SelfState = {
  session: SessionInfo;
  // self is the /v1/self answer for the signed-in person: null while not
  // signed in or not read yet, "error" when the read failed.
  self: SelfAnswer | null | "error";
  refreshSelf: () => Promise<void>;
  enrollment: Enrollment | null;
  // setEnrollment binds a fresh record into the approver lane, or drops it.
  setEnrollment: (rec: Enrollment | null) => void;
  storage: { usable: boolean; outlook: StorageOutlook };
  openSignIn: () => void;
  // signedIn hands the shell a check-in a tab completed itself, so the
  // header and the head follow it without a reload.
  signedIn: (resp: CheckinResponse) => void;
  notice: Notice | null;
  setNotice: (n: Notice | null) => void;
  // sheets is whether the setup dialog, under the key enable, and the Add a
  // phone sheet are open. Home and Devices open them and the shell draws
  // them, so a press before a tab has loaded is not lost and a setup that
  // left to sign in opens again on whichever tab the sign-in returns to.
  sheets: { enable: boolean; phone: boolean };
  setSheet: (which: "enable" | "phone", open: boolean) => void;
  setCount: (tab: SelfTab, n: number | null) => void;
  // waiting is what the queue last read as the reader's to decide: the
  // requests this browser can sign, or before setup the ones a device of
  // the reader could decide. null while unread, "error" after a failed read
  // the queue shows.
  waiting: ApprovalRow[] | null | "error";
  setWaiting: (rows: ApprovalRow[] | null | "error") => void;
  // servers is the shell's own read of the person's credentials and each
  // sponsored agent's: null while not read, "error" when it failed.
  // readServers reads them again.
  servers: CredentialRow[] | null | "error";
  readServers: () => void;
};

export const SelfContext = React.createContext<SelfState | null>(null);

// useSelf answers the shared state; every tab renders inside the shell.
export function useSelf(): SelfState {
  const s = React.useContext(SelfContext);
  if (!s) throw new Error("useSelf outside the self-service shell");
  return s;
}

// Standing is what the person's account may set up, as the page knows it:
// nobody signed in, the /v1/self read in flight, that read failed, or its
// two answers.
export type Standing =
  | { kind: "signed-out" }
  | { kind: "reading" }
  | { kind: "error" }
  | { kind: "read"; browser: boolean; mobile: boolean };

// standingOf reads the standing off the shared state. A session that is
// still resuming counts as signed out, as the tabs draw it.
export function standingOf({ session, self }: Pick<SelfState, "session" | "self">): Standing {
  if (session.kind !== "signed-in") return { kind: "signed-out" };
  if (self === null) return { kind: "reading" };
  if (self === "error") return { kind: "error" };
  const channels = self.enroll_channels || [];
  return { kind: "read", browser: channels.includes("browser"), mobile: channels.includes("mobile") };
}
