import * as React from "react";
import { PencilIcon, PlusIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PageHead } from "@/components/page-head";
import { ConfigTab } from "@/components/settings-config";
import { RegistryTab } from "@/components/settings-registry";
import { TokensTab } from "@/components/settings-tokens";
import { listApiTokens, listAttestationHashes } from "@/lib/api";
import { DRAFT_TITLE } from "@/lib/config-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { adminAreas } from "@/lib/session";
import { NEW_TOKEN, type SettingsTab, TABS, hiddenTabsLine, hiddenTokensTabLine, rendersOf } from "@/lib/settings-words";

// Settings is the configuration read-out, the attestation registry and the
// API tokens: one tab
// each with the tab in the address, the tab's own action in the page head,
// and the tabs a delegated seat cannot read left out with one line saying
// which grant they need.

const route = routeByKey("settings");

type Props = { tab?: string };

// Counts are the numbers in the tab labels. null is a count this seat may
// not read or a read that failed, and the label then carries no number.
type Counts = { registry: number | null; tokens: number | null };

export function Settings({ tab }: Props) {
  const areas = adminAreas();
  const canConfig = !areas || !!areas.config;
  const canTokens = !areas || !!areas.tokens;
  const tabs = TABS.filter((t) => (t.area === "config" ? canConfig : canTokens));
  const hidden = TABS.filter((t) => !tabs.includes(t));
  const current: SettingsTab = tabs.some((t) => t.key === tab) ? (tab as SettingsTab) : tabs[0].key;

  const [counts, setCounts] = React.useState<Counts>({ registry: null, tokens: null });
  // Each action button moves its tab's counter; the tab opens its sheet
  // when the counter moves, so the head owns the action and the tab owns
  // the panel it opens.
  const [mintRequest, setMintRequest] = React.useState(0);
  const [draftRequest, setDraftRequest] = React.useState(0);

  // The counts ride their own reads and never fail the page: a count that
  // could not be read is left off the label, and the tab's own body says
  // what went wrong.
  React.useEffect(() => {
    let alive = true;
    if (canConfig) {
      listAttestationHashes().then(
        (rows) => { if (alive) setCounts((c) => ({ ...c, registry: rendersOf(rows || []).length })); },
        () => undefined,
      );
    }
    if (canTokens) {
      listApiTokens().then(
        (rows) => { if (alive) setCounts((c) => ({ ...c, tokens: (rows || []).length })); },
        () => undefined,
      );
    }
    return () => { alive = false; };
  }, [canConfig, canTokens]);

  const hiddenLine = hidden.length === 0 ? ""
    : hidden[0].area === "config" ? hiddenTabsLine(hidden.map((t) => t.label))
      : hiddenTokensTabLine(hidden[0].label);

  const actions = current === "tokens"
    ? <Button onClick={() => setMintRequest((n) => n + 1)}><PlusIcon /> {NEW_TOKEN}</Button>
    : current === "configuration"
      ? <Button variant="outline" onClick={() => setDraftRequest((n) => n + 1)}><PencilIcon /> {DRAFT_TITLE}</Button>
      : undefined;

  const countOf = (key: SettingsTab) => (key === "registry" ? counts.registry : key === "tokens" ? counts.tokens : null);

  return (
    <>
      <PageHead label={route.label} description={route.description} actions={actions} />
      <div className="flex flex-col gap-4 px-6 py-5">
        <Tabs value={current} onValueChange={(v) => navigate("settings", [v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              {tabs.map((t) => {
                const n = countOf(t.key);
                return (
                  <TabsTrigger key={t.key} value={t.key}>
                    {t.label} {n !== null && <span className="font-mono text-xs text-muted-foreground">{n}</span>}
                  </TabsTrigger>
                );
              })}
            </TabsList>
          </div>
          {hiddenLine && <p className="text-[13px] text-muted-foreground" data-hidden-tabs>{hiddenLine}</p>}
          {tabs.some((t) => t.key === "configuration") && (
            <TabsContent value="configuration"><ConfigTab draftRequest={draftRequest} /></TabsContent>
          )}
          {tabs.some((t) => t.key === "registry") && (
            <TabsContent value="registry"><RegistryTab /></TabsContent>
          )}
          {tabs.some((t) => t.key === "tokens") && (
            <TabsContent value="tokens"><TokensTab mintRequest={mintRequest} /></TabsContent>
          )}
        </Tabs>
      </div>
    </>
  );
}
