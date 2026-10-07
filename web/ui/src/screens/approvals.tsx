import * as React from "react";
import { SmartphoneIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PageHead } from "@/components/page-head";
import { RequestsTab } from "@/components/approvals-requests";
import { DevicesTab } from "@/components/approver-devices";
import { ChannelsTab } from "@/components/channels-tab";
import { getConfig, listApprovals, listApproverDevices } from "@/lib/api";
import { type Seat, mine, own } from "@/lib/approval-model";
import { ADD_PHONE, type ApprovalsTab, TABS } from "@/lib/approval-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { snapshot } from "@/lib/session";

// Approvals is the area's page: three tabs with the tab in the address,
// the counts in the tab
// labels, and the page's one action, Add a phone, in the head while the
// devices tab is open. The whole area sits behind the approvals grant, so
// no tab is ever hidden on its own.

const route = routeByKey("approvals");

type Props = {
  tab?: string;
  // openID is a request named in the address (approvals/requests/<id>), from
  // Overview or an audit record: the queue opens that row's sheet once it
  // holds the row.
  openID?: string;
};

// Counts are the numbers in the tab labels. null is a read that has not
// answered or failed, and the label then carries no number.
type Counts = { waiting: number | null; devices: number | null };

const COUNT_MS = 5000;

export function Approvals({ tab, openID }: Props) {
  const current: ApprovalsTab = TABS.some((t) => t.key === tab) ? (tab as ApprovalsTab) : "requests";
  const here = snapshot();
  const seat: Seat = { user: here ? here.user : "", roles: here ? here.roles : [] };
  const [counts, setCounts] = React.useState<Counts>({ waiting: null, devices: null });
  // The Add a phone button moves this counter; the devices tab opens its
  // sheet when the counter moves, so the head owns the action and the tab
  // owns the panel.
  const [addRequest, setAddRequest] = React.useState(0);
  const [nonce, setNonce] = React.useState(0);
  // unsignedOwn is approval.unsignedOwnDecisions as the server runs it. The
  // console carries no device signature, so with the switch off the server
  // refuses a person's own request here. A read that fails, which a seat
  // without the config area gets, keeps the strict answer, the default.
  const [unsignedOwn, setUnsignedOwn] = React.useState(false);
  React.useEffect(() => {
    let alive = true;
    getConfig().then(
      (c) => { if (alive) setUnsignedOwn(!!(c.approval && c.approval.unsigned_own_decisions)); },
      () => undefined,
    );
    return () => { alive = false; };
  }, []);

  // The counts ride their own reads and never fail the page: a count that
  // could not be read is left off the label, and the tab's own body says
  // what went wrong. The waiting count polls, since it is the number the
  // sidebar and the tab exist for.
  React.useEffect(() => {
    let alive = true;
    const readWaiting = () => listApprovals("pending").then(
      (a) => { if (alive) setCounts((c) => ({ ...c, waiting: (a.approvals || []).length })); },
      () => undefined,
    );
    void readWaiting();
    const t = setInterval(() => { if (document.visibilityState !== "hidden") void readWaiting(); }, COUNT_MS);
    listApproverDevices().then(
      (rows) => { if (alive) setCounts((c) => ({ ...c, devices: (rows || []).length })); },
      () => undefined,
    );
    return () => { alive = false; clearInterval(t); };
  }, [nonce]);

  const actions = current === "devices"
    ? <Button onClick={() => setAddRequest((n) => n + 1)}><SmartphoneIcon /> {ADD_PHONE}</Button>
    : undefined;

  const countOf = (key: ApprovalsTab) => (key === "requests" ? counts.waiting : key === "devices" ? counts.devices : null);

  return (
    <>
      <PageHead label={route.label} description={route.description} actions={actions} />
      <div className="flex flex-col gap-4 px-6 py-5">
        <Tabs value={current} onValueChange={(v) => navigate("approvals", [v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              {TABS.map((t) => {
                const n = countOf(t.key);
                return (
                  <TabsTrigger key={t.key} value={t.key} data-tab={t.key}>
                    {t.label} {n !== null && <span className="font-mono text-xs text-muted-foreground" data-count={t.key}>{n}</span>}
                  </TabsTrigger>
                );
              })}
            </TabsList>
          </div>
          <TabsContent value="requests">
            <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} needsDevice={(r) => !unsignedOwn && own(r, seat)} openID={openID} onChanged={() => setNonce((n) => n + 1)} />
          </TabsContent>
          <TabsContent value="devices">
            <DevicesTab addRequest={addRequest} onChanged={() => setNonce((n) => n + 1)} />
          </TabsContent>
          <TabsContent value="channels">
            <ChannelsTab onSeeDevices={() => navigate("approvals", ["devices"])} />
          </TabsContent>
        </Tabs>
      </div>
    </>
  );
}
