import { Chip } from "@/components/chip";
import { RemoveInEntra } from "@/components/entra-account";
import { KIND_ICON, KindIcon } from "@/components/kind-icon";
import { FROM_ENTRA } from "@/lib/entra-words";
import type { Holding } from "@/lib/user-access";
import { ORIGIN_WORDS, throughRoles } from "@/lib/user-words";

// HolderSource is the Source cell of a role's Holders tab while the role
// has a group in Entra: where this holder's role comes from, each source
// once. A role from Entra is removed in Entra, since the next read would
// give it again, so its chip carries the link that opens the group there.
// An origin the console does not know shows as the server sent it.
export function HolderSource({ holding, groupID, group }: { holding: Holding; groupID: string; group: string }) {
  if (!holding.origins.length) {
    return holding.through.length ? <span className="text-caption whitespace-normal text-foreground" data-holder-source="through">{throughRoles(holding.through)}</span> : null;
  }
  return (
    <div className="flex flex-col items-start gap-1 text-caption whitespace-normal text-foreground" data-holder-source={holding.origins.join(" ")}>
      {holding.origins.map((o) => {
        if (o === "entra") {
          return (
            <span key={o} className="flex flex-col items-start gap-0.5">
              <Chip tone="plain" icon={KIND_ICON["from-entra"]}>{FROM_ENTRA}</Chip>
              {groupID && <RemoveInEntra groupID={groupID} group={group} />}
            </span>
          );
        }
        const known = o === "scim" || o === "admin";
        return (
          <span key={o} className="flex items-start gap-1.5">
            {known && <KindIcon kind={o === "scim" ? "from-idm" : "from-straza"} className="mt-px shrink-0 text-muted-foreground" />}
            {ORIGIN_WORDS[o] || o}
          </span>
        );
      })}
    </div>
  );
}
