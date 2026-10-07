import * as React from "react";
import { TriangleAlertIcon } from "lucide-react";
import { Button } from "@/components/ui/button";

type Props = { children: React.ReactNode };
type State = { error: Error | null };

// ScreenBoundary keeps a screen crash from white-paging the whole console:
// the sidebar and header survive, the broken screen shows what happened,
// and navigating away remounts fresh because the boundary is keyed by
// route.
export class ScreenBoundary extends React.Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div role="alert" className="mx-auto my-12 flex max-w-[560px] flex-col items-center gap-2 text-center" data-screen-error>
        <TriangleAlertIcon className="size-5 text-warn" aria-hidden="true" />
        <p className="font-semibold text-foreground">This screen hit an error.</p>
        <p className="break-words font-mono text-[13px] text-muted-foreground">{String(this.state.error.message || this.state.error)}</p>
        <p className="text-sm text-text-2">
          The rest of the console is unaffected: switch screens, or{" "}
          <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => this.setState({ error: null })}>try again</Button>.
        </p>
      </div>
    );
  }
}
