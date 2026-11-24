import * as React from "react";
import { LockIcon } from "lucide-react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Chip } from "@/components/chip";
import { PageHead } from "@/components/page-head";
import { ConfigTab } from "@/components/settings-config";
import { KeysTab } from "@/components/settings-keys";
import { RegistryTab } from "@/components/settings-registry";
import { TokensTab } from "@/components/settings-tokens";
import { listApiTokens, listAttestationHashes } from "@/lib/api";
import { ASSERTION_PURPOSE, type SigningKeyRow, listSigningKeys } from "@/lib/keys-api";
import { hiddenKeysTabLine } from "@/lib/keys-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { adminAreas } from "@/lib/session";
import { READ_ONLY, type SettingsTab, TABS, hiddenTabsLine, hiddenTokensTabLine, rendersOf } from "@/lib/settings-words";

// Settings is the API tokens, the signing keys, the attestation registry
// and the configuration read-out: one tab each with the tab in the
// address, each tab's action inside the tab, and the tabs a delegated
// seat cannot read left out with one line saying which grant they need.

const route = routeByKey("settings");

// row is the configuration row the address names after the tab, as in
// settings/configuration/floor, which the tab opens.
type Props = { tab?: string; row?: string };

// Counts are the numbers in the tab labels. null is a count this seat may
// not read or a read that failed, and the label then carries no number.
type Counts = { registry: number | null; tokens: number | null };

export function Settings({ tab, row }: Props) {
  const areas = adminAreas();
  const canConfig = !areas || !!areas.config;
  const canTokens = !areas || !!areas.tokens;
  const canKeys = !areas;
  const tabs = TABS.filter((t) => (t.area === "config" ? canConfig : t.area === "tokens" ? canTokens : canKeys));
  const hidden = TABS.filter((t) => !tabs.includes(t));
  const current: SettingsTab = tabs.some((t) => t.key === tab) ? (tab as SettingsTab) : tabs[0].key;

  const [counts, setCounts] = React.useState<Counts>({ registry: null, tokens: null });
  // keys is the client assertion key list, from the page's read and from
  // each read the tab makes, so the count follows a rotation. null is a
  // list this seat may not read or a read that failed.
  const [keys, setKeys] = React.useState<SigningKeyRow[] | null>(null);

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
    if (canKeys) {
      listSigningKeys().then(
        (rows) => { if (alive) setKeys((rows || []).filter((k) => k.purpose === ASSERTION_PURPOSE)); },
        () => undefined,
      );
    }
    return () => { alive = false; };
  }, [canConfig, canTokens, canKeys]);

  const hiddenConfig = hidden.filter((t) => t.area === "config").map((t) => t.label);
  const hiddenLine = [
    hiddenConfig.length ? hiddenTabsLine(hiddenConfig) : "",
    ...hidden.filter((t) => t.area === "tokens").map((t) => hiddenTokensTabLine(t.label)),
    ...hidden.filter((t) => t.area === "full").map((t) => hiddenKeysTabLine(t.label)),
  ].filter(Boolean).join(" ");

  const liveKeys = keys ? keys.filter((k) => k.status !== "retired").length : null;
  const countOf = (key: SettingsTab) => (key === "registry" ? counts.registry : key === "tokens" ? counts.tokens : key === "keys" ? liveKeys : null);

  return (
    <>
      <PageHead label={route.label} description={route.description} />
      <div className="flex flex-col gap-4 px-6 py-5">
        <Tabs value={current} onValueChange={(v) => navigate("settings", [v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              {tabs.map((t) => {
                const n = countOf(t.key);
                return (
                  <TabsTrigger key={t.key} value={t.key}>
                    {t.key === "configuration" && <LockIcon aria-hidden="true" />}
                    {t.label} {n !== null && <span className="font-mono text-caption leading-4 text-muted-foreground">{n}</span>}
                    {t.key === "configuration" && <Chip tone="plain">{READ_ONLY}</Chip>}
                  </TabsTrigger>
                );
              })}
            </TabsList>
          </div>
          {hiddenLine && <p className="text-caption text-muted-foreground" data-hidden-tabs>{hiddenLine}</p>}
          {tabs.some((t) => t.key === "configuration") && (
            <TabsContent value="configuration"><ConfigTab row={row} /></TabsContent>
          )}
          {tabs.some((t) => t.key === "registry") && (
            <TabsContent value="registry"><RegistryTab /></TabsContent>
          )}
          {tabs.some((t) => t.key === "tokens") && (
            <TabsContent value="tokens"><TokensTab /></TabsContent>
          )}
          {tabs.some((t) => t.key === "keys") && (
            <TabsContent value="keys"><KeysTab onRows={setKeys} /></TabsContent>
          )}
        </Tabs>
      </div>
    </>
  );
}
