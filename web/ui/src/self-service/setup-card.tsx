// SetupCard is the prompt to set this browser up, across the full width on
// Home and on Devices. It shows while this browser is not set up, keeps a
// key, and the account may set up a browser or nobody is signed in. A
// browser that will not keep a key gets a red line in its place, and a
// failed read of what the account may set up gets that sentence with Try
// again.
import { SmartphoneIcon } from "lucide-react";
import { KindTile } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import { standingOf, useSelf } from "./context";
import * as W from "./words";

export function SetupCard() {
  const { session, self, enrollment, storage, openSignIn, refreshSelf, setSheet } = useSelf();
  if (enrollment) return null;
  if (!storage.usable) {
    return <p className="border-l-[3px] border-danger bg-card px-4 py-3 text-dense leading-relaxed text-text-2" role="alert" data-no-storage>{W.NO_STORAGE}</p>;
  }
  const standing = standingOf({ session, self });
  if (standing.kind === "error") {
    return (
      <div className="flex flex-wrap items-center gap-3 rounded-md border border-input bg-secondary px-4 py-2.5" data-eligibility-unread>
        <p className="m-0 min-w-0 flex-1 text-dense leading-relaxed text-text-2">{W.ELIGIBILITY_UNREAD}</p>
        <Button variant="outline" onClick={() => void refreshSelf()}>{W.TRY_AGAIN}</Button>
      </div>
    );
  }
  if (standing.kind === "reading" || (standing.kind === "read" && !standing.browser)) return null;
  const signedIn = standing.kind === "read";
  return (
    <section className="flex flex-col gap-4 rounded-md border border-link bg-card p-5 sm:flex-row sm:items-start" data-setup-card>
      <KindTile kind="browser" size="lg" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <h2 className="text-section leading-snug font-semibold text-foreground">{W.SETUP_HEAD}</h2>
        {!signedIn && <p className="m-0 max-w-[78ch] text-dense leading-relaxed text-text-2">{W.SETUP_SIGN_IN}</p>}
        <div className="mt-1 flex flex-wrap items-center gap-2">
          {signedIn
            ? <Button onClick={() => setSheet("enable", true)} data-setup-browser>{W.SETUP_BROWSER}</Button>
            : <Button onClick={openSignIn}>{W.SIGN_IN}</Button>}
          {standing.kind === "read" && standing.mobile && (
            <Button variant="outline" onClick={() => setSheet("phone", true)} data-use-phone><SmartphoneIcon /> {W.ADD_PHONE}</Button>
          )}
        </div>
      </div>
    </section>
  );
}
