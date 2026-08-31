import { useMemo, useState } from "react";
import { ArrowUpRight, CheckCircle2, ClipboardCheck, Clock3, Search, ShieldCheck, XCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import type { Task } from "@/lib/api";
import type { CarbonReviewStatus, CarbonReviewTarget, CarbonScopeInput } from "@/lib/carbon-api";
import { useI18n } from "@/lib/i18n";
import { useCarbonReviews, useDecideCarbonReview } from "@/lib/queries";
import { cn, timeAgo } from "@/lib/utils";

function reviewStatusLabel(status: CarbonReviewStatus, t: ReturnType<typeof useI18n>["t"]): string {
  if (status === "approved") return t("Approved", "已通过");
  if (status === "rejected") return t("Changes requested", "需要调整");
  return t("Waiting", "待审核");
}

function reviewTargetLabel(review: CarbonReviewTarget, task: Task | undefined, t: ReturnType<typeof useI18n>["t"]): string {
  if (review.targetKind === "manual_check") {
    const index = Number(review.checkId);
    const check = Number.isInteger(index) ? task?.checks?.[index] : undefined;
    return check?.desc || t("Manual check", "人工检查");
  }
  return task?.title || t("Task plan", "任务计划");
}

export function ReviewCenter({
  scope,
  tasks,
  onOpenTask,
}: {
  scope: CarbonScopeInput;
  tasks: Task[];
  onOpenTask?: (task: Task) => void;
}) {
  const { t } = useI18n();
  const reviewsQuery = useCarbonReviews(scope);
  const decide = useDecideCarbonReview(scope);
  const reviews = useMemo(
    () => reviewsQuery.data?.available ? reviewsQuery.data.data.reviews ?? [] : [],
    [reviewsQuery.data],
  );
  const [tab, setTab] = useState<"pending" | "resolved">("pending");
  const [query, setQuery] = useState("");
  const [decisionTarget, setDecisionTarget] = useState<{ review: CarbonReviewTarget; status: "approved" | "rejected" }>();

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return reviews.filter((review) => {
      if (tab === "pending" ? review.status !== "pending" : review.status === "pending") return false;
      const task = tasks.find((item) => item.id === review.taskId);
      if (!needle) return true;
      return [review.id, review.taskId, review.targetId, review.reviewerActor, review.decision, task?.title]
        .filter(Boolean).join(" ").toLocaleLowerCase().includes(needle);
    });
  }, [query, reviews, tab, tasks]);
  const pendingCount = reviews.filter((review) => review.status === "pending").length;

  return (
    <div className="flex h-full min-w-0 flex-col bg-panel">
      <header className="flex min-h-14 shrink-0 flex-wrap items-center justify-between gap-3 border-b px-4 py-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <ClipboardCheck className="size-4 shrink-0 text-brand" />
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">{t("Review center", "审核中心")}</h1>
            <p className="truncate text-xs text-muted-foreground">{t("A reviewer checks a task plan or a manual checkpoint here—not a lease request.", "审核者在这里检查任务计划或人工检查点；这里不是任务认领审批。")}</p>
          </div>
        </div>
        <div className="relative min-w-40 flex-1 sm:w-64 sm:flex-none">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input value={query} onChange={(event) => setQuery(event.target.value)} className="h-8 pl-8" placeholder={t("Search reviews", "搜索审核")} />
        </div>
      </header>

      <div className="flex shrink-0 items-center justify-between gap-3 border-b px-4 py-2">
        <Tabs value={tab} onValueChange={(value) => setTab(value as "pending" | "resolved")}>
          <TabsList>
            <TabsTrigger value="pending">{t("Waiting", "待审核")}<Badge variant="secondary" className="ml-1">{pendingCount}</Badge></TabsTrigger>
            <TabsTrigger value="resolved">{t("Reviewed", "已处理")}</TabsTrigger>
          </TabsList>
        </Tabs>
        <p className="hidden text-xs text-muted-foreground md:block">{t("Start a review from the task details so Carbon can bind the exact target.", "请从任务详情发起审核，Carbon 会自动绑定准确对象。")}</p>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {!reviewsQuery.data?.available && !reviewsQuery.isLoading ? (
          <Empty className="m-4 min-h-64"><EmptyHeader><EmptyMedia variant="icon"><ShieldCheck /></EmptyMedia><EmptyTitle>{t("Reviews are unavailable", "审核功能暂不可用")}</EmptyTitle><EmptyDescription>{t("Update the local Carbon service and reopen this project.", "更新本地 Carbon 服务后重新打开项目。")}</EmptyDescription></EmptyHeader></Empty>
        ) : filtered.length === 0 ? (
          <Empty className="m-4 min-h-64"><EmptyHeader><EmptyMedia variant="icon"><ClipboardCheck /></EmptyMedia><EmptyTitle>{tab === "pending" ? t("Nothing is waiting for review", "目前没有待审核内容") : t("No review decisions yet", "还没有审核结论")}</EmptyTitle><EmptyDescription>{tab === "pending" ? t("Open a task and choose “Request review” when its plan or a manual checkpoint needs a second pair of eyes.", "打开任务，在计划或人工检查点需要确认时选择“发起审核”。") : t("Approved and returned items will remain here for later reference.", "通过和退回的记录会保留在这里，方便之后回看。")}</EmptyDescription></EmptyHeader></Empty>
        ) : (
          <main className="mx-auto grid w-full max-w-5xl gap-3 p-4">
            {filtered.map((review) => {
              const task = tasks.find((item) => item.id === review.taskId);
              return (
                <Card key={review.id} size="sm" className={cn(review.status === "pending" && "border-brand/25")}>
                  <CardHeader>
                    <div className="flex min-w-0 items-start gap-3">
                      <div className={cn("grid size-9 shrink-0 place-items-center rounded-xl bg-muted text-muted-foreground", review.status === "approved" && "bg-emerald-500/10 text-emerald-600", review.status === "rejected" && "bg-destructive/10 text-destructive", review.status === "pending" && "bg-brand/10 text-brand")}>
                        {review.status === "approved" ? <CheckCircle2 /> : review.status === "rejected" ? <XCircle /> : <Clock3 />}
                      </div>
                      <div className="min-w-0">
                        <CardTitle className="truncate">{reviewTargetLabel(review, task, t)}</CardTitle>
                        <CardDescription className="mt-1 flex flex-wrap items-center gap-1.5">
                          <Badge variant="outline">{review.targetKind === "plan" ? t("Plan", "计划") : t("Manual check", "人工检查")}</Badge>
                          <span>{t("Reviewer", "审核者")}：{review.reviewerActor}</span><span>·</span><span>{timeAgo(review.createdAt)}</span>
                        </CardDescription>
                      </div>
                    </div>
                    <Badge variant={review.status === "pending" ? "secondary" : "outline"}>{reviewStatusLabel(review.status, t)}</Badge>
                  </CardHeader>
                  <CardContent className="grid gap-3">
                    {task && (
                      <button type="button" disabled={!onOpenTask} onClick={() => onOpenTask?.(task)} className={cn("flex min-w-0 items-center gap-2 rounded-xl border bg-muted/20 px-3 py-2 text-left", onOpenTask && "hover:bg-muted/55 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}>
                        <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{task.id}</span><span className="truncate text-sm">{task.title}</span><ArrowUpRight className="ml-auto size-3.5 shrink-0 text-muted-foreground" />
                      </button>
                    )}
                    {review.decision && <div className="rounded-xl bg-muted/35 px-3 py-2"><p className="text-xs font-medium">{t("Review note", "审核意见")}</p><p className="mt-1 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">{review.decision}</p></div>}
                    {review.status === "pending" && (
                      <div className="flex flex-wrap justify-end gap-2">
                        <Button size="sm" variant="outline" onClick={() => setDecisionTarget({ review, status: "rejected" })}><XCircle />{t("Request changes", "退回调整")}</Button>
                        <Button size="sm" onClick={() => setDecisionTarget({ review, status: "approved" })}><CheckCircle2 />{t("Approve", "通过")}</Button>
                      </div>
                    )}
                  </CardContent>
                </Card>
              );
            })}
          </main>
        )}
      </div>

      <ReviewDecisionDialog target={decisionTarget} pending={decide.isPending} onClose={() => setDecisionTarget(undefined)} onSubmit={(decision) => decisionTarget && decide.mutate({ id: decisionTarget.review.id, status: decisionTarget.status, decision }, { onSuccess: (result) => result.available && setDecisionTarget(undefined) })} />
    </div>
  );
}

function ReviewDecisionDialog({
  target,
  pending,
  onClose,
  onSubmit,
}: {
  target?: { review: CarbonReviewTarget; status: "approved" | "rejected" };
  pending: boolean;
  onClose: () => void;
  onSubmit: (decision: string) => void;
}) {
  const { t } = useI18n();
  const [decision, setDecision] = useState("");
  const isApprove = target?.status === "approved";
  return (
    <Dialog open={Boolean(target)} onOpenChange={(open) => { if (!open && !pending) { setDecision(""); onClose(); } }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader><DialogTitle>{isApprove ? t("Approve this review?", "确认通过？") : t("What needs to change?", "哪里还需要调整？")}</DialogTitle><DialogDescription>{isApprove ? t("Leave a short conclusion so the team knows what was checked.", "留一句简短结论，让团队知道你确认了什么。") : t("Write a concrete next step. The item remains in the review history after it is returned.", "请写清楚下一步要改什么；退回后仍会保留在审核记录里。")}</DialogDescription></DialogHeader>
        <FieldGroup><Field><FieldLabel htmlFor="review-decision">{t("Review note", "审核意见")}</FieldLabel><Textarea id="review-decision" autoFocus value={decision} onChange={(event) => setDecision(event.target.value)} rows={4} placeholder={isApprove ? t("Checked the plan and…", "已确认计划中的……") : t("Please adjust…", "请调整……")} /></Field></FieldGroup>
        <DialogFooter><Button variant="outline" disabled={pending} onClick={onClose}>{t("Cancel", "取消")}</Button><Button variant={isApprove ? "default" : "destructive"} disabled={!decision.trim() || pending} onClick={() => onSubmit(decision.trim())}>{isApprove ? <CheckCircle2 /> : <XCircle />}{isApprove ? t("Confirm approval", "确认通过") : t("Return for changes", "退回调整")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
