// YAML scalars are typed before serialization. Strings use JSON quoting,
// which is valid YAML and cannot inject keys, comments or additional lines.
export type ConfigScalar = string | number | boolean;
export type ConfigEntry = { key: string; value: ConfigScalar };
type Tree = Map<string, Tree | ConfigScalar>;

export function configFragment(entries: ConfigEntry[]): string {
  const root: Tree = new Map();
  for (const { key, value } of entries) {
    let node = root;
    const parts = key.split(".");
    parts.forEach((part, i) => {
      if (i === parts.length - 1) { node.set(part, value); return; }
      const next = node.get(part);
      if (next instanceof Map) { node = next; return; }
      const child: Tree = new Map(); node.set(part, child); node = child;
    });
  }
  function render(tree: Tree, depth: number): string[] {
    const pad = "  ".repeat(depth);
    return [...tree].flatMap(([key, value]) => value instanceof Map
      ? [pad + key + ":", ...render(value, depth + 1)]
      : [pad + key + ": " + JSON.stringify(value)]);
  }
  return render(root, 0).join("\n");
}

export function draftScalar(id: string, value: string): ConfigScalar {
  if (["events", "jit", "ownDecisions"].includes(id)) return value === "true";
  if (id === "hold") return Number(value);
  return value;
}

export function draftValueError(id: string, value: string): string {
  if (!value) return "";
  if (id === "hold" && (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)))) return "Enter a whole number of seconds, zero or greater.";
  if (["grace", "upstream", "ret"].includes(id) && !/^(?:0|(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+)$/.test(value)) return "Use a duration such as 30s, 5m or 24h.";
  return "";
}
