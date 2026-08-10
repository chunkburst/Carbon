import { useEffect } from "react";
import { useEditor, EditorContent, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import Placeholder from "@tiptap/extension-placeholder";
import { Markdown } from "tiptap-markdown";
import {
  Bold,
  Code,
  Code2,
  Heading1,
  Heading2,
  Italic,
  Link as LinkIcon,
  List,
  ListOrdered,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

type MarkdownEditorImplProps = {
  value: string;
  onChange: (md: string) => void;
  placeholder?: string;
  minHeight: string;
  defaultPlaceholder: string;
};

// tiptap-markdown adds a `markdown` storage with getMarkdown(); it isn't typed in v3.
function getMarkdown(editor: Editor): string {
  return (editor.storage as { markdown?: { getMarkdown(): string } }).markdown?.getMarkdown() ?? "";
}

export function MarkdownEditorImpl({
  value,
  onChange,
  placeholder,
  minHeight,
  defaultPlaceholder,
}: MarkdownEditorImplProps) {
  const editor = useEditor({
    // React 19 can mount, disconnect, and reconnect passive effects while a lazy
    // component is resolving. Creating TipTap during render lets its scheduled
    // cleanup destroy the command manager before the reconnect effect applies
    // options, which crashes the whole task-detail route. Build the editor from
    // the mounted effect instead so the instance lifecycle follows the component.
    immediatelyRender: false,
    extensions: [
      StarterKit.configure({ link: { openOnClick: false } }),
      Placeholder.configure({ placeholder: placeholder ?? defaultPlaceholder }),
      Markdown.configure({ html: false, linkify: true, transformPastedText: true }),
    ],
    content: value,
    onUpdate: ({ editor }) => onChange(getMarkdown(editor)),
    editorProps: {
      attributes: {
        class: "prose prose-sm dark:prose-invert max-w-none focus:outline-none px-3 py-2",
        style: `min-height:${minHeight}`,
      },
    },
  });

  // Sync external value changes (e.g. reset to "" after submit) without clobbering typing.
  useEffect(() => {
    // React StrictMode may reconnect this passive effect with the previous TipTap
    // instance after its scheduled cleanup has already destroyed the command manager.
    if (!editor || editor.isDestroyed) return;
    if (value !== getMarkdown(editor)) {
      editor.commands.setContent(value || "");
    }
  }, [value, editor]);

  return (
    <div className="overflow-hidden rounded-md border focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50">
      {editor && <Toolbar editor={editor} />}
      <EditorContent editor={editor} />
    </div>
  );
}

function Toolbar({ editor }: { editor: Editor }) {
  const { t } = useI18n();
  const btn = (
    active: boolean,
    onClick: () => void,
    Icon: typeof Bold,
    label: string,
  ) => (
    <button
      type="button"
      aria-label={label}
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
      className={cn(
        "grid size-7 place-items-center rounded text-muted-foreground hover:bg-foreground/10 hover:text-foreground",
        active && "bg-foreground/10 text-foreground",
      )}
    >
      <Icon className="size-3.5" />
    </button>
  );

  const chain = () => editor.chain().focus();

  return (
    <div className="flex flex-wrap items-center gap-0.5 border-b bg-muted/30 px-1.5 py-1">
      {btn(editor.isActive("bold"), () => chain().toggleBold().run(), Bold, t("Bold", "粗体"))}
      {btn(editor.isActive("italic"), () => chain().toggleItalic().run(), Italic, t("Italic", "斜体"))}
      {btn(editor.isActive("code"), () => chain().toggleCode().run(), Code, t("Inline code", "行内代码"))}
      <span className="mx-0.5 h-4 w-px bg-border" />
      {btn(editor.isActive("heading", { level: 1 }), () => chain().toggleHeading({ level: 1 }).run(), Heading1, t("Heading 1", "一级标题"))}
      {btn(editor.isActive("heading", { level: 2 }), () => chain().toggleHeading({ level: 2 }).run(), Heading2, t("Heading 2", "二级标题"))}
      <span className="mx-0.5 h-4 w-px bg-border" />
      {btn(editor.isActive("bulletList"), () => chain().toggleBulletList().run(), List, t("Bullet list", "无序列表"))}
      {btn(editor.isActive("orderedList"), () => chain().toggleOrderedList().run(), ListOrdered, t("Numbered list", "有序列表"))}
      {btn(editor.isActive("codeBlock"), () => chain().toggleCodeBlock().run(), Code2, t("Code block", "代码块"))}
      {btn(editor.isActive("link"), () => toggleLink(editor, t("Link URL", "链接地址")), LinkIcon, t("Link", "链接"))}
    </div>
  );
}

function toggleLink(editor: Editor, prompt: string) {
  if (editor.isActive("link")) {
    editor.chain().focus().unsetLink().run();
    return;
  }
  const url = window.prompt(prompt);
  if (url) editor.chain().focus().setLink({ href: url }).run();
}
