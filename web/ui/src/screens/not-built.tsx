import { LockIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { PageHead } from "@/components/page-head";
import { type Route } from "@/lib/routes";
import { navigate } from "@/lib/router";

type Props = {
  route: Route;
  reachable: boolean;
  first: Route | null;
};

// NotBuilt stands in for an area this build does not include, and for an
// area a delegated admin's grants do not cover. Both keep the page head so
// the address still names its page.
export function NotBuilt({ route, reachable, first }: Props) {
  const label = route.label;
  return (
    <>
      <PageHead label={label} description={route.description} />
      {reachable ? (
        <EmptyState icon={route.icon} title={label + " is not built yet."} />
      ) : (
        <EmptyState
          icon={LockIcon}
          title={label + " is not available to this account."}
          action={first && <Button variant="outline" onClick={() => navigate(first.key)}>{"Open " + first.label}</Button>}
        >
          {"Your role does not include " + label + ". Ask an administrator for a role that does."}
        </EmptyState>
      )}
    </>
  );
}
