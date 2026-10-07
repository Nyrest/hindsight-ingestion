import { Compass } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";

export default function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-[60svh] items-center justify-center">
      <EmptyState
        className="w-full max-w-md"
        icon={Compass}
        title={t("notFound.title")}
        description={t("notFound.description")}
        action={
          <Button asChild>
            <Link to="/">{t("notFound.home")}</Link>
          </Button>
        }
      />
    </div>
  );
}
