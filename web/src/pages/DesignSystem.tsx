import { useState } from "react";
import { Moon, Sun, MoreHorizontal, Plus, Check } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Toaster } from "@/components/ui/sonner";
import { toast } from "sonner";
import { getTheme, toggleTheme, type Theme } from "@/lib/theme";

export default function DesignSystem() {
  const [theme, setTheme] = useState<Theme>(getTheme());

  return (
    <TooltipProvider delayDuration={200}>
      <div className="min-h-screen bg-background text-foreground">
        <header className="sticky top-0 z-10 border-b bg-background/80 backdrop-blur">
          <div className="mx-auto flex max-w-4xl items-center justify-between px-6 py-4">
            <div>
              <h1 className="text-lg font-semibold tracking-tight">Carbon design system</h1>
              <p className="text-sm text-muted-foreground">
                shadcn/ui · Linear-style · the single source of UI truth
              </p>
            </div>
            <Button
              variant="outline"
              size="icon"
              aria-label="Toggle theme"
              onClick={() => setTheme(toggleTheme())}
            >
              {theme === "dark" ? <Sun /> : <Moon />}
            </Button>
          </div>
        </header>

        <main className="mx-auto max-w-4xl space-y-12 px-6 py-10">
          <Section title="Typography" hint="Inter Variable · tight tracking on headings">
            <div className="space-y-2">
              <h1 className="text-3xl font-semibold tracking-tight">The quick brown fox</h1>
              <h2 className="text-2xl font-semibold tracking-tight">The quick brown fox</h2>
              <h3 className="text-lg font-semibold">The quick brown fox</h3>
              <p className="text-sm">
                Body text. Calm, legible, and dense enough to scan quickly.
              </p>
              <p className="text-sm text-muted-foreground">
                Muted text for secondary information and metadata.
              </p>
              <p className="font-mono text-sm">PROJ-128 · mono for ids and code</p>
            </div>
          </Section>

          <Section title="Color" hint="Semantic tokens — never hardcode hex">
            <div className="flex flex-wrap gap-4">
              {SWATCHES.map((s) => (
                <Swatch key={s.name} name={s.name} className={s.className} />
              ))}
            </div>
          </Section>

          <Section title="Buttons" hint="variant × size">
            <div className="flex flex-wrap items-center gap-3">
              <Button>Primary</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="outline">Outline</Button>
              <Button variant="ghost">Ghost</Button>
              <Button variant="destructive">Destructive</Button>
              <Button variant="link">Link</Button>
            </div>
            <div className="mt-4 flex flex-wrap items-center gap-3">
              <Button size="sm">Small</Button>
              <Button>Default</Button>
              <Button size="lg">Large</Button>
              <Button size="icon" aria-label="Add">
                <Plus />
              </Button>
              <Button disabled>Disabled</Button>
            </div>
          </Section>

          <Section title="Badges" hint="status & labels">
            <div className="flex flex-wrap items-center gap-3">
              <Badge>Default</Badge>
              <Badge variant="secondary">Secondary</Badge>
              <Badge variant="outline">Outline</Badge>
              <Badge variant="destructive">Destructive</Badge>
              <Badge className="bg-brand text-brand-foreground">Brand</Badge>
            </div>
          </Section>

          <Section title="Forms" hint="inputs & selects">
            <div className="grid max-w-sm gap-4">
              <div className="grid gap-1.5">
                <Label htmlFor="title">Task title</Label>
                <Input id="title" placeholder="Add idempotency keys…" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="status">Status</Label>
                <Select>
                  <SelectTrigger id="status">
                    <SelectValue placeholder="Select status" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="backlog">Backlog</SelectItem>
                    <SelectItem value="in_progress">In progress</SelectItem>
                    <SelectItem value="done">Done</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          </Section>

          <Section title="Overlays & feedback" hint="dialog · menu · tooltip · toast">
            <div className="flex flex-wrap items-center gap-3">
              <Dialog>
                <DialogTrigger asChild>
                  <Button variant="outline">Open dialog</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader>
                    <DialogTitle>Create task</DialogTitle>
                    <DialogDescription>
                      A focused modal for a single decision.
                    </DialogDescription>
                  </DialogHeader>
                  <div className="grid gap-1.5">
                    <Label htmlFor="d-title">Title</Label>
                    <Input id="d-title" placeholder="What needs doing?" />
                  </div>
                  <DialogFooter>
                    <Button>Create</Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>

              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="icon" aria-label="Actions">
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuLabel>Actions</DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem>Claim</DropdownMenuItem>
                  <DropdownMenuItem>Run checks</DropdownMenuItem>
                  <DropdownMenuItem className="text-destructive">Cancel</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>

              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="ghost">Hover me</Button>
                </TooltipTrigger>
                <TooltipContent>Tooltips explain, they don't decorate.</TooltipContent>
              </Tooltip>

              <Button
                variant="secondary"
                onClick={() => toast.success("Task PROJ-128 moved to Done")}
              >
                Show toast
              </Button>
            </div>
          </Section>

          <Section title="Composition" hint="a Linear-style task row, built only from tokens + primitives">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Ready to start</CardTitle>
                <CardDescription>Tasks whose dependencies are all closed.</CardDescription>
              </CardHeader>
              <CardContent className="p-0">
                <Separator />
                {TASKS.map((t) => (
                  <TaskRow key={t.id} {...t} />
                ))}
              </CardContent>
            </Card>
            <div className="mt-4 max-w-md space-y-2">
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-2/3" />
            </div>
          </Section>
        </main>
        <Toaster />
      </div>
    </TooltipProvider>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section>
      <div className="mb-4 flex items-baseline justify-between">
        <h2 className="text-sm font-semibold tracking-tight">{title}</h2>
        {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
      </div>
      {children}
    </section>
  );
}

function Swatch({ name, className }: { name: string; className: string }) {
  return (
    <div className="flex flex-col items-center gap-1.5">
      <div className={`size-14 rounded-lg ${className}`} />
      <span className="font-mono text-xs text-muted-foreground">{name}</span>
    </div>
  );
}

const SWATCHES = [
  { name: "background", className: "bg-background border" },
  { name: "card", className: "bg-card border" },
  { name: "muted", className: "bg-muted" },
  { name: "secondary", className: "bg-secondary" },
  { name: "accent", className: "bg-accent" },
  { name: "primary", className: "bg-primary" },
  { name: "brand", className: "bg-brand" },
  { name: "destructive", className: "bg-destructive" },
  { name: "border", className: "bg-border" },
];

type Task = {
  id: string;
  title: string;
  status: "backlog" | "in_progress" | "done";
  who?: string;
};

const TASKS: Task[] = [
  { id: "PROJ-101", title: "Add idempotency keys to payment webhook", status: "in_progress", who: "CL" },
  { id: "PROJ-102", title: "Document the check runner contract", status: "backlog" },
  { id: "PROJ-103", title: "Wire the task board to /api/tasks", status: "done", who: "SH" },
];

function TaskRow({ id, title, status, who }: Task) {
  return (
    <div className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-muted/50">
      <span className="w-20 shrink-0 font-mono text-xs text-muted-foreground">{id}</span>
      <span className="flex-1 truncate text-sm">{title}</span>
      <StatusBadge status={status} />
      {who ? (
        <Avatar className="size-6">
          <AvatarFallback className="text-[10px]">{who}</AvatarFallback>
        </Avatar>
      ) : (
        <div className="size-6" />
      )}
    </div>
  );
}

function StatusBadge({ status }: { status: Task["status"] }) {
  if (status === "done")
    return (
      <Badge variant="secondary" className="gap-1">
        <Check className="size-3" /> Done
      </Badge>
    );
  if (status === "in_progress")
    return <Badge className="bg-brand text-brand-foreground">In progress</Badge>;
  return <Badge variant="outline">Backlog</Badge>;
}
