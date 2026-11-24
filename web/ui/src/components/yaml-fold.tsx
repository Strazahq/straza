import { ChevronRightIcon, CodeXmlIcon } from "lucide-react";
import { APP_YAML_NOTE, VIEW_YAML } from "@/lib/save-words";

// Data is the data-* attributes a caller hands to the document's block,
// the hooks that tests read.
type Data = { [key: `data-${string}`]: string | number | boolean | undefined };

// YamlFold is the one fold of a raw document on a create step or a change
// sheet: a closed line that says View as YAML with a muted note beside it,
// and the document in a mono block that scrolls inside its own box. It is
// a native details element, so the document stays in the page while it is
// closed and the browser's find reaches it.
export function YamlFold({ note, text, ...data }: Data & { note: string; text: string }) {
  return (
    <details className="group/yaml min-w-0 rounded-md border border-border bg-background" data-yaml-fold>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption text-foreground outline-none hover:bg-secondary focus-visible:ring-[3px] focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
        <ChevronRightIcon aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open/yaml:rotate-90" />
        <CodeXmlIcon aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0">
          <span className="font-semibold">{VIEW_YAML}</span>
          <span className="ml-1.5 text-muted-foreground">{note}</span>
        </span>
      </summary>
      <pre className="m-0 max-h-[420px] overflow-auto border-t border-border px-3.5 py-2.5 font-mono text-caption leading-relaxed text-text-2" {...data}>{text}</pre>
    </details>
  );
}

// ManifestFold is the app.yaml the answers so far would send, under the
// steps of Add MCP server and on every Change sheet of a server's page.
export function ManifestFold({ yaml }: { yaml: string }) {
  return <YamlFold note={APP_YAML_NOTE} text={yaml} data-manifest />;
}
