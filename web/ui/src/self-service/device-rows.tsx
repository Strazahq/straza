// The rows of the Devices tab: this browser, read from its own record, and
// each other device of the person, read from the server's list. A row is a
// kind tile, the name, one line, and its quiet buttons on the right, which
// drop under the line on a narrow window.
import { BellIcon, BellOffIcon } from "lucide-react";
import { Chip } from "@/components/chip";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import type { SelfDevice } from "@/lib/api";
import { agoLong } from "@/lib/words";
import * as W from "./words";

const ROW = "flex flex-col gap-3 rounded-md border border-border bg-card p-4 sm:flex-row sm:items-center";
const NAME = "font-semibold text-foreground [overflow-wrap:anywhere]";
const LINE = "m-0 text-dense leading-relaxed text-text-2";

// Push is what this browser can be told: whether notifications can be
// offered at all and why not, and whether they are on.
export type Push = { available: boolean; why: string; on: boolean };

// ThisBrowserRow is this browser's row. Where notifications cannot be
// offered, the reason sits behind a help icon beside the line, and a
// subscription that could not be renewed says so under it.
export function ThisBrowserRow({ name, user, setUpAt, push, lost, busy, onTurnOn, onTurnOff, onRemove }: {
  name: string;
  user: string;
  setUpAt: string;
  push: Push;
  lost: string;
  busy: boolean;
  onTurnOn: () => void;
  onTurnOff: () => void;
  onRemove: () => void;
}) {
  const state = !push.available ? "none" : push.on ? "on" : "off";
  return (
    <li className={ROW} data-this-browser>
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <KindTile kind="browser" size="md" />
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-2">
            <span className={NAME}>{name}</span>
            <Chip tone="accent">{W.THIS_BROWSER_BADGE}</Chip>
          </span>
          <p className={LINE + " flex flex-wrap items-center gap-x-1"}>
            <span data-device-line>{W.deviceLine(user, state, agoLong(setUpAt))}</span>
            {!push.available && <HelpTip label={W.ABOUT_NOTIFICATIONS} text={push.why} />}
          </p>
          {lost && <p className="m-0 text-caption leading-snug text-muted-foreground" data-push-lost>{lost}</p>}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 sm:justify-end">
        {push.available && (push.on
          ? <Button variant="ghost" disabled={busy} onClick={onTurnOff} data-turn-off><BellOffIcon /> {W.TURN_OFF}</Button>
          : <Button variant="ghost" disabled={busy} onClick={onTurnOn} data-turn-on><BellIcon /> {W.TURN_ON}</Button>)}
        <Button variant="ghost" aria-label={W.removeName(name)} onClick={onRemove}>{W.REMOVE}</Button>
      </div>
    </li>
  );
}

// hardware says the platform attested a key kept in secure hardware: a key
// level other than software, with an attestation that is set and not none.
export const hardware = (d: SelfDevice) => d.key_security_level !== "software" && !!d.attestation && d.attestation !== "none";

// OtherRow is a device of the person that is not this browser.
export function OtherRow({ device, onRemove }: { device: SelfDevice; onRemove: () => void }) {
  return (
    <li className={ROW} data-device={device.id}>
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <KindTile kind={device.platform === "browser" ? "browser" : "phone"} size="md" />
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className={NAME}>{device.name}</span>
          <p className={LINE} data-device-line>{W.otherDeviceLine(hardware(device), device.notifies, device.last_seen ? agoLong(device.last_seen) : "")}</p>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 sm:justify-end">
        <Button variant="ghost" aria-label={W.removeName(device.name)} onClick={onRemove}>{W.REMOVE}</Button>
      </div>
    </li>
  );
}
