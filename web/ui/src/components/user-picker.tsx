import * as React from "react";
import { UserIcon, XIcon } from "lucide-react";
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Button } from "@/components/ui/button";
import { type ApiError, type UserRow, listUsers, query } from "@/lib/api";
import { cn } from "@/lib/utils";

export type PickedUser = { id: string; username: string };

type Props = {
  value: PickedUser | null;
  onChange: (u: PickedUser | null) => void;
  label: string;
  placeholder?: string;
  className?: string;
};

type Found = { kind: "idle" } | { kind: "loading" } | { kind: "ready"; rows: UserRow[] } | { kind: "error"; message: string };

// UserPicker finds one user by the server search (q over username, email
// and external id, eight rows), so a filter by user never needs the whole
// directory in the browser. A picked user shows as a chip with a clear
// button; the filter sends the id.
export function UserPicker({ value, onChange, label, placeholder = "Type a name", className }: Props) {
  const [open, setOpen] = React.useState(false);
  const [q, setQ] = React.useState("");
  const [found, setFound] = React.useState<Found>({ kind: "idle" });
  const seq = React.useRef(0);

  React.useEffect(() => {
    if (!open) return;
    const my = ++seq.current;
    const needle = q.trim();
    if (!needle) { setFound({ kind: "idle" }); return; }
    setFound({ kind: "loading" });
    const t = setTimeout(() => {
      listUsers(query({ q: needle, limit: 8, sort: "name", order: "asc" })).then(
        (page) => { if (my === seq.current) setFound({ kind: "ready", rows: page.items || [] }); },
        (e: ApiError) => { if (my === seq.current) setFound({ kind: "error", message: e.unreachable ? "strazad did not answer." : e.message }); },
      );
    }, 250);
    return () => clearTimeout(t);
  }, [q, open]);

  if (value) {
    return (
      <span className={cn("inline-flex h-9 items-center gap-1.5 rounded-md border border-border bg-background pl-2.5 pr-1 text-sm", className)} data-user-picker="picked">
        <UserIcon className="size-3.5 text-muted-foreground" aria-hidden="true" />
        <span className="text-muted-foreground">{label}</span>
        <span className="font-mono">{value.username}</span>
        <Button variant="ghost" size="icon-xs" aria-label={"Clear the " + label.toLowerCase() + " filter"} onClick={() => onChange(null)}><XIcon /></Button>
      </span>
    );
  }
  return (
    <div className={cn("relative", className)} data-user-picker="open">
      <Command shouldFilter={false} label={label} className="h-9 rounded-md border border-border bg-background">
        <CommandInput placeholder={label + ": " + placeholder} value={q} onValueChange={setQ} onFocus={() => setOpen(true)} onBlur={() => setTimeout(() => setOpen(false), 150)} aria-label={label} className="h-9" />
        {open && q.trim() && (
          <CommandList className="absolute left-0 top-10 z-20 w-72 rounded-md border border-border bg-popover shadow-md">
            {found.kind === "loading" && <CommandItem value="loading" disabled>Searching the directory.</CommandItem>}
            {found.kind === "error" && <CommandItem value="error" disabled>{"The directory could not be searched: " + found.message}</CommandItem>}
            {found.kind === "ready" && found.rows.length === 0 && <CommandEmpty>No user matches.</CommandEmpty>}
            {found.kind === "ready" && found.rows.map((u) => (
              <CommandItem key={u.id} value={u.id} onSelect={() => { onChange({ id: u.id, username: u.username }); setQ(""); setOpen(false); }}>
                <span className="font-mono">{u.username}</span>
                {u.display && <span className="text-muted-foreground">{u.display}</span>}
              </CommandItem>
            ))}
          </CommandList>
        )}
      </Command>
    </div>
  );
}
