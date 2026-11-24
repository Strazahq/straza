// The read of the person's own devices that Home and Devices share. It
// takes the sign-in session, so it runs only while one is live. A server
// that does not have the read yet answers 404, which leaves the list
// unread with no error, as for a person who is signed out.
import * as React from "react";
import { type ApiError, type SelfDevice, selfDevices } from "@/lib/api";

// Devices is the list as last read: null while it is not read or cannot be
// read, and failed after a read that went wrong. reread reads it again.
export type Devices = { list: SelfDevice[] | null; failed: boolean; reread: () => void };

export function useDevices(signedIn: boolean): Devices {
  const [list, setList] = React.useState<SelfDevice[] | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [reads, setReads] = React.useState(0);

  React.useEffect(() => {
    if (!signedIn) {
      setList(null);
      setFailed(false);
      return undefined;
    }
    let alive = true;
    selfDevices().then(
      (a) => {
        if (!alive) return;
        setList((a && a.devices) || []);
        setFailed(false);
      },
      (e: ApiError) => {
        if (!alive) return;
        setList(null);
        // A 401 has already ended the session, and an older server has no
        // list to give: neither is a failure to show.
        setFailed(e.status !== 404 && e.status !== 401);
      },
    );
    return () => { alive = false; };
  }, [signedIn, reads]);

  const reread = React.useCallback(() => setReads((n) => n + 1), []);
  return { list, failed, reread };
}

// approvesElsewhere says this browser is not set up while another device of
// the person approves: the list is read and holds at least one device. While
// the list is being read, and after its read failed, it says no.
export const approvesElsewhere = (setUp: boolean, devices: Devices) => !setUp && !!devices.list && devices.list.length > 0;
