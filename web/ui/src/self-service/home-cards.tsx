// Home's three cards: what waits for the person to decide, the credentials
// that wait for them, and the devices they approve from. Each says its
// state in one line, at most one sentence, and at most one button.
import * as React from "react";
import { CircleCheckIcon, HourglassIcon, KeyRoundIcon, type LucideIcon } from "lucide-react";
import { KindTile } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import type { ApprovalRow } from "@/lib/api";
import { callOf, shortID, timeLeft } from "@/lib/approval-model";
import { askVerb } from "@/lib/approval-words";
import { cn } from "@/lib/utils";
import { READING } from "./credential-words";
import { type CredentialRow, expired, rank, sortRows } from "./credential-rows";
import { standingOf, useSelf } from "./context";
import { support } from "./push";
import { navigate } from "./router";
import { type Devices, approvesElsewhere } from "./self-devices";
import * as W from "./words";

const TICK_MS = 1000;

// Tile is a plain square of a kind tile's size with an icon inside: the
// warn tile of something that waits for the person, and the neutral tile
// of the check.
export function Tile({ icon: Icon, tone }: { icon: LucideIcon; tone: "warn" | "plain" }) {
  return (
    <span className={cn("inline-grid size-7 shrink-0 place-items-center rounded-md", tone === "warn" ? "bg-warn-bg text-warn" : "bg-secondary text-foreground")} data-tile={tone}>
      <Icon className="size-4" aria-hidden="true" />
    </span>
  );
}

function Card({ name, tile, line, sentence, button }: { name: string; tile: React.ReactNode; line: string; sentence?: string; button?: React.ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col gap-3 rounded-md border border-border bg-card p-4" data-card={name}>
      <div className="flex items-center gap-2.5">
        {tile}
        <h2 className="text-body leading-snug font-semibold text-foreground" data-card-line>{line}</h2>
      </div>
      {sentence && <p className="m-0 flex-1 text-dense leading-relaxed text-text-2 [overflow-wrap:anywhere]" data-card-sentence>{sentence}</p>}
      {button && <div className="mt-auto">{button}</div>}
    </section>
  );
}

// firstOf is the waiting request that runs out first.
const firstOf = (rows: ApprovalRow[]) => [...rows].sort((a, b) => Date.parse(a.expiresAt) - Date.parse(b.expiresAt))[0];

// beforeSetupWords picks the sentence of a waiting request before this
// browser is set up: for an account that approves from a phone only, for
// one that already approves from another device, and for one that may set
// up a browser but not a phone.
const beforeSetupWords = (phoneOnly: boolean, elsewhere: boolean, browserOnly: boolean) =>
  (phoneOnly ? (elsewhere ? W.answerOnPhone : W.answerAfterPhone) : elsewhere ? W.answerOnDevice : browserOnly ? W.answerAfterBrowser : W.answerAfterSetup);

// RequestsCard is the first card. Set up, it counts what waits for the
// person and names the request that runs out first. Before setup it
// counts what the server would let a device of theirs decide, or says
// that nothing waits and how a request would be answered, or asks a
// signed-out visitor to sign in. A read that failed takes the place of
// nothing waiting.
export function RequestsCard({ onReview, devices }: { onReview: () => void; devices: Devices }) {
  const { session, self, enrollment, waiting } = useSelf();
  const rows = Array.isArray(waiting) ? waiting : [];
  const elsewhere = approvesElsewhere(!!enrollment, devices);
  const standing = standingOf({ session, self });
  const phoneOnly = standing.kind === "read" && !standing.browser && standing.mobile;
  const browserOnly = standing.kind === "read" && !standing.mobile;
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (rows.length === 0) return undefined;
    const t = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(t);
  }, [rows.length]);

  const signedIn = session.kind === "signed-in";
  if (rows.length > 0 && (enrollment || signedIn)) {
    const r = firstOf(rows);
    const who = r.username || r.user;
    const call = callOf(r.summary).call;
    return (
      <Card
        name="requests"
        tile={<Tile icon={HourglassIcon} tone="warn" />}
        line={W.waitsForYou(rows.length)}
        sentence={enrollment ? W.firstWaiting(who, askVerb(r), call, timeLeft(r, now)) : beforeSetupWords(phoneOnly, elsewhere, browserOnly)(who, askVerb(r), call)}
        button={enrollment ? <Button onClick={onReview} data-review>{W.REVIEW}</Button> : undefined}
      />
    );
  }
  // nothing is the card when no request waits, with the sentence of the
  // state. A read that failed says so under the tile of a request, since
  // the tile of the state would say that all is well.
  const nothing = (tile: React.ReactNode, sentence?: string) => (waiting === "error"
    ? <Card name="requests" tile={<KindTile kind="hold" size="md" />} line={W.REQUESTS_UNREAD_CARD} />
    : <Card name="requests" tile={tile} line={W.NOTHING_WAITS_CARD} sentence={sentence} />);
  if (enrollment) return nothing(<Tile icon={CircleCheckIcon} tone="plain" />);
  if (standing.kind === "read" && !standing.browser && standing.mobile) return nothing(<KindTile kind="phone" size="md" />, W.WAITS_SHOW_HERE_PHONE);
  if (standing.kind === "read" && !standing.browser) return nothing(<KindTile kind="hold" size="md" />, W.NOT_ENROLLED_NO_ROLE);
  if (standing.kind === "signed-out") {
    return <Card name="requests" tile={<KindTile kind="hold" size="md" />} line={W.SIGN_IN_FOR_REQUESTS} sentence={W.SIGN_IN_FOR_REQUESTS_BODY} />;
  }
  return nothing(<KindTile kind="hold" size="md" />, elsewhere ? W.WAITS_SHOW_HERE_DEVICE : browserOnly ? W.WAITS_SHOW_HERE_BROWSER : W.WAITS_SHOW_HERE);
}

// waitingLine is the sentence of the first credential that waits for the
// person, by what is missing.
function waitingLine(r: CredentialRow): string {
  if (r.connected && expired(r)) return W.tokenExpired(r.app, r.who);
  if (r.who) return W.agentNoToken(r.who, r.app);
  return r.kind === "oauth" ? W.noSignInFromYou(r.app) : W.noTokenFromYou(r.app);
}

// CredentialsCard is the second card, read from the shell's own read of
// the credentials, the same rows and the same ranking as the table.
export function CredentialsCard() {
  const { session, servers, openSignIn, readServers } = useSelf();
  const key = (tone: "warn" | "plain") => (tone === "warn" ? <Tile icon={KeyRoundIcon} tone="warn" /> : <KindTile kind="credential" size="md" />);
  if (session.kind !== "signed-in") {
    return <Card name="credentials" tile={key("plain")} line={W.CREDENTIALS_SIGN_IN} button={<Button variant="outline" onClick={openSignIn}>{W.SIGN_IN}</Button>} />;
  }
  if (servers === "error") {
    return <Card name="credentials" tile={key("plain")} line={W.CREDENTIALS_UNREAD} button={<Button variant="outline" onClick={readServers}>{W.TRY_AGAIN}</Button>} />;
  }
  if (servers === null) return <Card name="credentials" tile={key("plain")} line={READING} />;
  const waits = sortRows(servers).filter((r) => rank(r, servers) === 0);
  if (waits.length === 0) return <Card name="credentials" tile={key("plain")} line={W.CREDENTIALS_SET} />;
  return (
    <Card
      name="credentials"
      tile={key("warn")}
      line={W.needsCredential(waits.length)}
      sentence={waitingLine(waits[0])}
      button={<Button variant="outline" onClick={() => navigate("credentials")} data-set-it>{W.OPEN_CREDENTIALS}</Button>}
    />
  );
}

// DevicesCard is the third card: the person's devices when the server
// lists them, this browser alone when it does not, and nothing for an
// account with no role to approve.
export function DevicesCard({ devices }: { devices: Devices }) {
  const { session, self, enrollment, storage, setSheet } = useSelf();
  const standing = standingOf({ session, self });
  const list = devices.list;
  const manage = <Button variant="outline" onClick={() => navigate("devices")} data-manage>{W.MANAGE}</Button>;

  if (list && list.length > 0) {
    const here = enrollment ? enrollment.deviceId : "";
    const ordered = [...list.filter((d) => d.id === here), ...list.filter((d) => d.id !== here)];
    const nameOf = (id: string, name: string) => (id === here ? W.THIS_BROWSER_NAME : name);
    const alone = ordered.length === 1 && ordered[0].id === here;
    // An account with no role to approve keeps its devices, which approve
    // nothing, so the line only counts them.
    const noRole = standing.kind === "read" && !standing.browser && !standing.mobile;
    return (
      <Card
        name="devices"
        tile={<KindTile kind={alone ? "browser" : "phone"} size="md" />}
        line={noRole ? W.devicesSetUp(ordered.length) : W.approveFrom(ordered.length)}
        sentence={W.devicesLine(ordered.map((d) => nameOf(d.id, d.name)), ordered.filter((d) => !d.notifies).map((d) => nameOf(d.id, d.name)))}
        button={manage}
      />
    );
  }
  if (enrollment) {
    const can = support(enrollment, storage.outlook);
    const push = !can.available ? "none" : enrollment.push ? "on" : "off";
    return <Card name="devices" tile={<KindTile kind="browser" size="md" />} line={W.APPROVE_FROM_HERE} sentence={W.hereLine(enrollment.deviceName || shortID(enrollment.deviceId), push)} button={manage} />;
  }
  if (standing.kind === "read" && !standing.browser) {
    if (!standing.mobile) return null;
    return (
      <Card
        name="devices"
        tile={<KindTile kind="phone" size="md" />}
        line={W.NO_PHONE_YET}
        sentence={W.PHONE_APPROVES}
        button={<Button variant="outline" onClick={() => setSheet("phone", true)} data-add-phone>{W.ADD_PHONE}</Button>}
      />
    );
  }
  const read = list !== null;
  return <Card name="devices" tile={<KindTile kind="browser" size="md" />} line={read ? W.NO_DEVICE_YET : W.NOT_SET_UP} />;
}
