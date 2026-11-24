import { FileQuestionIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { PageHead } from "@/components/page-head";
import { landing } from "@/lib/routes";
import { isPlainClick, navigate, pathFor } from "@/lib/router";

// NotFound renders inside the shell for an address off the route table.
export function NotFound({ path }: { path: string }) {
  return (
    <>
      <PageHead label="Page not found" description={null} />
      <EmptyState
        icon={FileQuestionIcon}
        title={"No page at " + path}
        action={
          <Button variant="outline" asChild>
            <a
              href={pathFor(landing.key)}
              onClick={(e) => {
                if (!isPlainClick(e)) return;
                e.preventDefault();
                navigate(landing.key);
              }}
            >
              {"Open " + landing.label}
            </a>
          </Button>
        }
      />
    </>
  );
}
