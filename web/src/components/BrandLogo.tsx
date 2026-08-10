import type { SVGProps } from "react";
import { cn } from "@/lib/utils";

// Native, font-independent Carbon mark. The 64px geometry is shared by the app
// chrome and favicon: a quiet rounded hexagon, six carbon nodes, and an open C path.
export function BrandLogo({ className, title = "Carbon", ...props }: SVGProps<SVGSVGElement> & { title?: string }) {
  return (
    <svg
      viewBox="0 0 64 64"
      fill="none"
      role="img"
      aria-label={title}
      className={cn("text-brand", className)}
      {...props}
    >
      <title>{title}</title>
      <path
        d="M32 6.5 53.2 18.75v24.5L32 55.5 10.8 43.25v-24.5L32 6.5Z"
        stroke="currentColor"
        strokeWidth="3.5"
        strokeLinejoin="round"
      />
      <path d="M41.25 22.2a12.35 12.35 0 1 0 0 19.6" stroke="currentColor" strokeWidth="4.2" strokeLinecap="round" />
      <circle cx="32" cy="6.5" r="2" fill="currentColor" />
      <circle cx="53.2" cy="18.75" r="2" fill="currentColor" />
      <circle cx="53.2" cy="43.25" r="2" fill="currentColor" />
      <circle cx="32" cy="55.5" r="2" fill="currentColor" />
      <circle cx="10.8" cy="43.25" r="2" fill="currentColor" />
      <circle cx="10.8" cy="18.75" r="2" fill="currentColor" />
    </svg>
  );
}
