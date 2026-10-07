// The state the three tabs share with the shell: the session as the tabs
// may show it, the /v1/self answer, the enrolment record and the storage
// outlook, plus the shell's doors for a tab to open the sign-in, replace
// the enrolment, count its rows and post a page-level notice.
import * as React from "react";
import type { SelfAnswer } from "@/lib/api";
import type { CheckinResponse } from "@/lib/session";
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
  // signedIn hands the shell a check-in a tab completed itself, the enrol
  // sheet's sign-in, so the header and the head follow it without a reload.
  signedIn: (resp: CheckinResponse) => void;
  notice: Notice | null;
  setNotice: (n: Notice | null) => void;
  // sheets is whether the This browser tab's two sheets are open. The head
  // opens them and the tab draws them, and the state lives here so a press
  // before the tab has loaded is not lost.
  sheets: { enable: boolean; phone: boolean };
  setSheet: (which: "enable" | "phone", open: boolean) => void;
  setCount: (tab: SelfTab, n: number | null) => void;
};

export const SelfContext = React.createContext<SelfState | null>(null);

// useSelf answers the shared state; every tab renders inside the shell.
export function useSelf(): SelfState {
  const s = React.useContext(SelfContext);
  if (!s) throw new Error("useSelf outside the self-service shell");
  return s;
}
