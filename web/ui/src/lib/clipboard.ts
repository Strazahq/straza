import { COPY_REFUSED } from "./config-words";
import { notify } from "./notify";

// copyText puts a value on the clipboard and says so in a toast, or says
// that the browser refused, the one sentence every copy button on the
// console shares. A browser with no clipboard, such as a plain-http page
// off localhost, refuses the same way.
export function copyText(text: string, ok: string): void {
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (!clipboard) {
    notify.failed(COPY_REFUSED);
    return;
  }
  try {
    void clipboard.writeText(text).then(() => notify.ok(ok), () => notify.failed(COPY_REFUSED));
  } catch {
    notify.failed(COPY_REFUSED);
  }
}
