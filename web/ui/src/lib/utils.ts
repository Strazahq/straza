import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// cn merges Tailwind class lists the shadcn way: clsx for conditionals,
// tailwind-merge so a caller's override wins over the component default.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// downloadText hands the browser a file to save. A browser without object
// URLs, such as the test harness, gets no download.
export function downloadText(filename: string, text: string, type: string) {
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") return;
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
