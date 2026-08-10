import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

// Facet is a compact "Any / value" dropdown for board/list faceted filtering. An empty
// value means "Any" (no filter); selecting "any" clears it.
export function Facet({
  value,
  onChange,
  placeholder,
  children,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  children: React.ReactNode;
}) {
  return (
    <Select value={value || "any"} onValueChange={(v) => onChange(v === "any" ? "" : v)}>
      <SelectTrigger
        className={cn(
          "h-6 gap-1 border-0 bg-transparent px-2 text-xs hover:bg-foreground/5 focus-visible:ring-0",
          value && "bg-foreground/[0.06] text-foreground",
        )}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="any">{placeholder}</SelectItem>
        {children}
      </SelectContent>
    </Select>
  );
}
