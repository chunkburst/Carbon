import { Component, lazy, Suspense, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

export type MarkdownEditorProps = {
  value: string;
  onChange: (md: string) => void;
  placeholder?: string;
  minHeight?: string;
};

const MarkdownEditorImpl = lazy(() =>
  import("@/components/MarkdownEditorImpl").then((module) => ({
    default: module.MarkdownEditorImpl,
  })),
);

type MarkdownEditorErrorBoundaryProps = {
  children: ReactNode;
  fallback: ReactNode;
};

type MarkdownEditorErrorBoundaryState = {
  hasError: boolean;
};

type MarkdownEditorFallbackProps = {
  value: string;
  onChange: (md: string) => void;
  placeholder?: string;
  minHeight: string;
};

// TipTap is loaded lazily to keep non-editor routes fast. Keep its failures contained so an
// editor lifecycle error cannot take down the enclosing task-detail route.
class MarkdownEditorErrorBoundary extends Component<
  MarkdownEditorErrorBoundaryProps,
  MarkdownEditorErrorBoundaryState
> {
  state: MarkdownEditorErrorBoundaryState = { hasError: false };

  static getDerivedStateFromError(): MarkdownEditorErrorBoundaryState {
    return { hasError: true };
  }

  render() {
    return this.state.hasError ? this.props.fallback : this.props.children;
  }
}

function MarkdownEditorFallback({
  value,
  onChange,
  placeholder,
  minHeight,
}: MarkdownEditorFallbackProps) {
  const { t } = useI18n();
  const defaultPlaceholder = t("Write…", "开始输入…");

  return (
    <div className="flex flex-col gap-2">
      <Alert variant="destructive">
        <AlertTriangle />
        <AlertTitle>{t("Rich-text editor is unavailable", "富文本编辑器暂不可用")}</AlertTitle>
        <AlertDescription>
          {t("You can keep editing in Markdown.", "你仍可使用 Markdown 继续编辑。")}
        </AlertDescription>
      </Alert>
      <Textarea
        aria-label={t("Markdown editor", "Markdown 编辑器")}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder ?? defaultPlaceholder}
        style={{ minHeight }}
        value={value}
      />
    </div>
  );
}

// Keep the editor API synchronous for callers, but defer TipTap and its ProseMirror stack
// until an editor is actually visible. Most Carbon sessions stay on the task board, Worker,
// or Work Log routes and should not parse a full rich-text editor during startup.
export function MarkdownEditor({
  value,
  onChange,
  placeholder,
  minHeight = "5rem",
}: MarkdownEditorProps) {
  const { language, t } = useI18n();
  return (
    <MarkdownEditorErrorBoundary
      fallback={
        <MarkdownEditorFallback
          value={value}
          onChange={onChange}
          placeholder={placeholder}
          minHeight={minHeight}
        />
      }
    >
      <Suspense
        fallback={
          <div
            aria-busy="true"
            className="animate-pulse rounded-md border bg-muted/20"
            style={{ minHeight }}
          />
        }
      >
        <MarkdownEditorImpl
          key={language}
          value={value}
          onChange={onChange}
          placeholder={placeholder}
          minHeight={minHeight}
          defaultPlaceholder={t("Write…", "开始输入…")}
        />
      </Suspense>
    </MarkdownEditorErrorBoundary>
  );
}
