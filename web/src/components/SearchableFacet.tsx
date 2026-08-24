import { useState } from "react";
import { ChevronsUpDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type SearchableFacetProps = {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  options: readonly string[];
  searchPlaceholder?: string;
  emptyText?: string;
};

/**
 * A compact single-value facet for long option lists. The trigger follows the existing
 * board facets while the popover keeps cmdk's keyboard navigation and text filtering.
 */
export function SearchableFacet({
  value,
  onChange,
  placeholder,
  options,
  searchPlaceholder,
  emptyText,
}: SearchableFacetProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const normalizedOptions = [...new Set(options.filter(Boolean))];
  const selectedLabel = value || placeholder;
  const inputPlaceholder = searchPlaceholder ?? t("Search labels…", "搜索标签…");
  const noResultsText = emptyText ?? t("No matching label", "没有匹配的标签");

  const select = (nextValue: string) => {
    onChange(nextValue);
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="xs"
          aria-label={value ? `${placeholder}: ${value}` : placeholder}
          title={value || undefined}
          className={cn(
            "h-6 min-w-0 max-w-44 justify-between gap-1 px-2 text-xs font-normal",
            value && "bg-muted text-foreground",
          )}
        >
          <span className="truncate">{selectedLabel}</span>
          <ChevronsUpDown data-icon="inline-end" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 gap-0 overflow-hidden p-0">
        <Command>
          <CommandInput autoFocus aria-label={inputPlaceholder} placeholder={inputPlaceholder} />
          <CommandList className="max-h-64">
            <CommandEmpty>{noResultsText}</CommandEmpty>
            <CommandGroup>
              <CommandItem
                value={`__clear_facet__ ${placeholder}`}
                data-checked={!value || undefined}
                onSelect={() => select("")}
              >
                {placeholder}
              </CommandItem>
              {normalizedOptions.map((option) => (
                <CommandItem
                  key={option}
                  value={option}
                  data-checked={option === value || undefined}
                  onSelect={() => select(option)}
                >
                  {option}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
