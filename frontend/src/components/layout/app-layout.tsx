import { useQuery } from "@tanstack/react-query";
import { History, KeyRound, LayoutDashboard, ListChecks, Menu, Settings, Workflow } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { NavLink, Outlet, useLocation } from "react-router";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { qk } from "@/lib/query";
import { cn } from "@/lib/utils";
import { LanguageSelector } from "./language-selector";
import { ThemeToggle } from "./theme-toggle";

const NAV = [
  { to: "/", key: "nav.dashboard", icon: LayoutDashboard, end: true },
  { to: "/tasks", key: "nav.tasks", icon: ListChecks, end: false },
  { to: "/runs", key: "nav.runs", icon: History, end: false },
  { to: "/credentials", key: "nav.credentials", icon: KeyRound, end: false },
  { to: "/settings", key: "nav.settings", icon: Settings, end: false },
] as const;

function Brand() {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-2.5 px-2">
      <div className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm">
        <Workflow className="size-4" />
      </div>
      <div className="leading-tight">
        <div className="text-sm font-semibold tracking-tight">{t("app.name")}</div>
        <div className="text-[11px] text-muted-foreground">{t("app.tagline")}</div>
      </div>
    </div>
  );
}

function NavItems({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  return (
    <nav className="flex flex-col gap-0.5">
      {NAV.map(({ to, key, icon: Icon, end }) => (
        <NavLink
          key={to}
          to={to}
          end={end}
          onClick={onNavigate}
          className={({ isActive }) =>
            cn(
              "flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors",
              isActive
                ? "bg-sidebar-accent text-sidebar-accent-foreground"
                : "text-sidebar-foreground/75 hover:bg-sidebar-accent/60 hover:text-sidebar-accent-foreground",
            )
          }
        >
          <Icon className="size-4" />
          {t(key)}
        </NavLink>
      ))}
    </nav>
  );
}

function Footer() {
  const { t } = useTranslation();
  const health = useQuery({ queryKey: qk.health, queryFn: api.health, staleTime: 5 * 60_000, retry: false });
  return (
    <div className="flex items-center justify-between gap-2 px-2 text-xs text-muted-foreground">
      <span className="truncate">
        {health.data ? t("app.version", { version: health.data.version }) : " "}
        {health.data && !health.data.authEnabled && (
          <span className="ml-1.5 text-amber-600 dark:text-amber-400">· {t("app.authDisabled")}</span>
        )}
      </span>
      <ThemeToggle />
    </div>
  );
}

export function AppLayout() {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
  const current = NAV.find((n) => (n.end ? location.pathname === n.to : location.pathname.startsWith(n.to)));

  return (
    <div className="flex min-h-svh">
      {/* Desktop sidebar */}
      <aside className="sticky top-0 hidden h-svh w-60 shrink-0 flex-col gap-6 border-r border-sidebar-border bg-sidebar py-4 pr-3 pl-3 md:flex">
        <Brand />
        <div className="flex-1 overflow-y-auto">
          <NavItems />
        </div>
        <LanguageSelector className="px-2" />
        <Footer />
      </aside>

      {/* Mobile top bar + sheet */}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur md:hidden">
          <Button variant="ghost" size="icon-sm" onClick={() => setMobileOpen(true)} aria-label={t("nav.open")}>
            <Menu />
          </Button>
          <span className="truncate text-sm font-semibold">{current ? t(current.key) : t("app.name")}</span>
          <div className="ml-auto">
            <ThemeToggle />
          </div>
        </header>
        <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
          <SheetContent side="left" className="w-72 gap-0 bg-sidebar p-0">
            <SheetHeader className="sr-only">
              <SheetTitle>{t("nav.title")}</SheetTitle>
              <SheetDescription>{t("nav.description")}</SheetDescription>
            </SheetHeader>
            <div className="flex h-full flex-col gap-6 px-3 py-4">
              <Brand />
              <div className="flex-1">
                <NavItems onNavigate={() => setMobileOpen(false)} />
              </div>
              <LanguageSelector className="px-2" />
              <Footer />
            </div>
          </SheetContent>
        </Sheet>

        <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
