// Generated from shadcn/ui, MIT License, Copyright (c) 2023 shadcn.
// The license text is in the LICENSE file in this folder.
import {
  CircleCheckIcon,
  InfoIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react"
import type { CSSProperties } from "react"
import { Toaster as Sonner, type ToasterProps } from "sonner"

// Toaster is the shadcn sonner wrapper with next-themes removed: the page
// resolves its own theme (prefers-color-scheme or data-theme) and passes it
// in, so no theme library ships in the bundle. Every sheet opens on the
// right with its X at the top and its buttons at the bottom, so toasts sit
// at the bottom left, clear of both.
const Toaster = ({ ...props }: ToasterProps) => {
  return (
    <Sonner
      className="toaster group"
      position="bottom-left"
      icons={{
        success: <CircleCheckIcon className="size-4" />,
        info: <InfoIcon className="size-4" />,
        warning: <TriangleAlertIcon className="size-4" />,
        error: <OctagonXIcon className="size-4" />,
        loading: <Loader2Icon className="size-4 animate-spin" />,
      }}
      style={
        {
          "--normal-bg": "var(--surface)",
          "--normal-text": "var(--text)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius)",
        } as CSSProperties
      }
      {...props}
    />
  )
}

export { Toaster }
