import { LockIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { PageHead } from "@/components/page-head";
import { type Route } from "@/lib/routes";
import { navigate } from "@/lib/router";
import { lockedAreaLine } from "@/lib/server-words";

type Props = {
  route: Route;
  reachable: boolean;
  grants: string;
  first: Route | null;
  // administers counts the servers whose admin role the session holds, so
  // the locked panel of a server admin says what the account does reach.
  administers?: number;
};

// NotBuilt stands in for an area this build does not include, and for an
// area a delegated admin's grants do not cover. Both keep the page head so
// the address still names its page.
export function NotBuilt({ route, reachable, grants, first, administers = 0 }: Props) {
  const label = route.label;
  return (
    <>
      <PageHead label={label} description={route.description} />
      {reachable ? (
        <EmptyState icon={route.icon} title={label + " is not built yet."}>
          {"The " + label + " screen is not in this build. strazactl reaches the same records."}
        </EmptyState>
      ) : (
        <EmptyState
          icon={LockIcon}
          title={label + " is not available to this account."}
          action={first && <Button variant="outline" onClick={() => navigate(first.key)}>{"Open " + first.label}</Button>}
        >
          {administers > 0 && !grants
            ? lockedAreaLine(administers, label)
            : "This account's admin scopes cover " + (grants || "nothing") + ", which does not reach " + label + ". Ask for a wider role, or use strazactl."}
        </EmptyState>
      )}
    </>
  );
}
