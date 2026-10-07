// The Add MCP server wizard's validation: one zod schema over the whole
// Form, every rule that reads another answer in superRefine, and the
// fields each step checks, in the order the step draws them. The messages
// are the approved sentences; each says what is missing and how to fix it.
// zod/mini: the same schemas as the classic build in functional form,
// tree-shaken to what this file calls (about 5 KB gzipped instead of 25).
import * as z from "zod/mini";
import type { FieldPath } from "react-hook-form";
import { type Form, NAME_RE, NAME_RULE, needsSecret, sentFor } from "@/lib/manifest";

export const MISSING = {
  name: "Name the server. " + NAME_RULE,
  imported: "Paste the registry's server.json and click Convert, or pick one of the other three cards.",
  url: "Say where Straza reaches it: an http or https address.",
  scheme: "The address must start with http:// or https://.",
  exec: "Say which executable Straza starts, as an absolute path on the strazad host.",
  image: "Name the image Straza runs.",
  secret: "Type the secret the server expects. It is stored sealed after the install.",
  envName: "Name the environment variable the process reads the secret from.",
  provider: "Pick the identity provider people sign in through.",
};

// headerMissing names what travels in the header: a person's token on a
// token server, the shared secret otherwise.
export const headerMissing = (cred: string) => "Name the header the " + (cred === "token" ? "token" : "secret") + " travels in.";

const text = z.string();

export const formSchema = z
  .object({
    name: text, desc: text, mode: text, runtime: text, url: text, exec: text, args: text, image: text,
    imported: z.custom<Form["imported"]>(),
    cred: text, agents: text, secret: text, sentAs: text, headerName: text, envName: text, provider: text, scopes: text,
  })
  .check(z.superRefine((f, ctx) => {
    const say = (path: keyof Form, message: string) => ctx.addIssue({ code: "custom", path: [path], message });
    const name = f.name.trim();
    if (!name) say("name", MISSING.name);
    else if (!NAME_RE.test(name)) say("name", NAME_RULE);
    // The import card is open until its record is converted, and the
    // conversion opens the runtime card it read, so an open import card
    // always means a paste that is not converted yet.
    if (f.mode === "import") say("mode", MISSING.imported);
    else if (f.runtime === "remote") {
      const url = f.url.trim();
      if (!url) say("url", MISSING.url);
      else if (!/^https?:\/\//i.test(url)) say("url", MISSING.scheme);
    } else if (f.runtime === "command" && !f.exec.trim()) say("exec", MISSING.exec);
    else if (f.runtime === "oci" && !f.image.trim()) say("image", MISSING.image);
    if (needsSecret(f) && !f.secret) say("secret", MISSING.secret);
    if (f.cred === "static" || f.cred === "token") {
      const sent = sentFor(f);
      if (sent === "env" && !f.envName.trim()) say("envName", MISSING.envName);
      if (sent === "header" && !f.headerName.trim()) say("headerName", headerMissing(f.cred));
    }
    if (f.cred === "oauth" && !f.provider) say("provider", MISSING.provider);
  }));

export type Step = "server" | "credential" | "review" | "check";
export const STEP_KEYS: Step[] = ["server", "credential", "review", "check"];

// stepFields lists what a step's primary checks, first drawn first, so the
// first invalid one is the one the click focuses. A token server draws its
// header before the shared secret; a static server the other way round.
export function stepFields(step: Step, f: Form): FieldPath<Form>[] {
  if (step === "server") return ["name", "mode", "url", "exec", "image"];
  if (step !== "credential") return [];
  return f.cred === "token" ? ["headerName", "secret"] : ["secret", "headerName", "envName", "provider"];
}
