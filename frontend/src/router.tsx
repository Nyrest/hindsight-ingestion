import { createBrowserRouter } from "react-router";
import { AppLayout } from "@/components/layout/app-layout";
import NotFoundPage from "@/pages/not-found-page";

// Pages are loaded on demand so the initial bundle stays small.
const page = (load: () => Promise<{ default: React.ComponentType }>) => async () => ({ Component: (await load()).default });

export const router = createBrowserRouter([
  {
    path: "/",
    element: <AppLayout />,
    children: [
      { index: true, lazy: page(() => import("@/pages/dashboard-page")) },
      { path: "credentials", lazy: page(() => import("@/pages/credentials-page")) },
      { path: "tasks", lazy: page(() => import("@/pages/tasks-page")) },
      {
        path: "tasks/new",
        lazy: async () => ({ Component: (await import("@/pages/task-edit-page")).NewTaskPage }),
      },
      {
        path: "tasks/:id",
        lazy: async () => ({ Component: (await import("@/pages/task-edit-page")).EditTaskPage }),
      },
      { path: "runs", lazy: page(() => import("@/pages/runs-page")) },
      { path: "runs/:id", lazy: page(() => import("@/pages/run-detail-page")) },
      { path: "settings", lazy: page(() => import("@/pages/settings-page")) },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);
