import { FileQuestion } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useTask } from "@/features/tasks/api";
import { TaskEditor } from "@/features/tasks/task-editor";
import { errorMessage, isApiError } from "@/lib/api";

export function NewTaskPage() {
  return <TaskEditor />;
}

export function EditTaskPage() {
  const { t } = useTranslation();
  const { id } = useParams();
  const task = useTask(id);

  if (task.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-64" />
        <div className="grid gap-6 lg:grid-cols-[190px_1fr]">
          <Skeleton className="hidden h-64 lg:block" />
          <div className="space-y-6">
            <Skeleton className="h-48" />
            <Skeleton className="h-72" />
          </div>
        </div>
      </div>
    );
  }
  if (task.isError || !task.data) {
    const notFound = isApiError(task.error) && task.error.status === 404;
    return (
      <EmptyState
        icon={FileQuestion}
        title={notFound ? t("taskEditor.notFound") : t("common.loadFailed")}
        description={notFound ? undefined : errorMessage(task.error)}
        action={
          <Button asChild variant="outline">
            <Link to="/tasks">{t("common.backTo", { page: t("nav.tasks") })}</Link>
          </Button>
        }
      />
    );
  }
  // key ensures the form resets when navigating between tasks
  return <TaskEditor key={task.data.id} task={task.data} />;
}
