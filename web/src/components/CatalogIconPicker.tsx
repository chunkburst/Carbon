import { useState } from "react";
import { RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  CatalogIcon,
  catalogBuiltinIconKeys,
  catalogEmojiIconKeys,
  catalogIconsEqual,
  type CatalogIconToken,
} from "@/components/CatalogIcon";
import { useI18n } from "@/lib/i18n";

const labels: Record<string, readonly [string, string]> = {
  folder: ["Folder", "文件夹"],
  layers: ["Layers", "层叠"],
  monitor: ["Desktop", "桌面"],
  smartphone: ["Mobile", "移动端"],
  apple: ["Apple", "苹果"],
  globe: ["Web", "网页"],
  server: ["Server", "服务器"],
  package: ["Package", "组件包"],
  database: ["Database", "数据库"],
  flask: ["Experiment", "实验"],
  atom: ["Atom", "原子"],
  rocket: ["Rocket", "火箭"],
  spark: ["Spark", "火花"],
  puzzle: ["Puzzle", "拼图"],
  shield: ["Shield", "盾牌"],
  palette: ["Palette", "调色板"],
};

export function CatalogIconPicker({
  value,
  assetURL,
  onChange,
  disabled = false,
  ariaLabel,
}: {
  value: CatalogIconToken | null | undefined;
  /** Optional same-origin project asset preview. Token choices remain local-only. */
  assetURL?: string;
  onChange: (icon: CatalogIconToken | null) => void;
  disabled?: boolean;
  ariaLabel: string;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const current = value ?? null;

  const choose = (icon: CatalogIconToken) => {
    onChange(icon);
    setOpen(false);
  };

  const iconLabel = (key: string) => {
    const label = labels[key];
    return label ? t(label[0], label[1]) : key;
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          disabled={disabled}
          aria-label={ariaLabel}
          title={ariaLabel}
        >
          <CatalogIcon icon={current} assetURL={assetURL} />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-3">
        <div className="flex flex-col gap-3">
          <div>
            <p className="text-sm font-medium">{t("Choose icon", "选择图标")}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{t("Choose from Carbon's built-in icon set.", "从 Carbon 内置图标集中选择。")}</p>
          </div>

          <PickerGroup
            label={t("Built-in", "内置")}
            values={catalogBuiltinIconKeys.map((key) => ({ kind: "builtin" as const, key }))}
            current={current}
            onChoose={choose}
            labelFor={iconLabel}
          />
          <PickerGroup
            label={t("Emoji", "表情")}
            values={catalogEmojiIconKeys.map((key) => ({ kind: "emoji" as const, key }))}
            current={current}
            onChoose={choose}
            labelFor={iconLabel}
          />

          {current && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="justify-start"
              onClick={() => {
                onChange(null);
                setOpen(false);
              }}
            >
              <RotateCcw data-icon="inline-start" />
              {t("Clear icon", "清除图标")}
            </Button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function PickerGroup({
  label,
  values,
  current,
  onChoose,
  labelFor,
}: {
  label: string;
  values: CatalogIconToken[];
  current: CatalogIconToken | null;
  onChoose: (icon: CatalogIconToken) => void;
  labelFor: (key: string) => string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <div className="grid grid-cols-8 gap-1">
        {values.map((icon) => {
          const label = labelFor(icon.key);
          return (
            <Button
              key={`${icon.kind}:${icon.key}`}
              type="button"
              variant={catalogIconsEqual(current, icon) ? "secondary" : "ghost"}
              size="icon-sm"
              aria-label={label}
              title={label}
              onClick={() => onChoose(icon)}
            >
              <CatalogIcon icon={icon} />
            </Button>
          );
        })}
      </div>
    </div>
  );
}
