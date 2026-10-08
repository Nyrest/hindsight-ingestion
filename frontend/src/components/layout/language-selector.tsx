import { Globe2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import i18n, { languages, languageNames, normalizeLanguage } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export function LanguageSelector({ className }: { className?: string }) {
  const { t } = useTranslation();
  const currentLanguage = normalizeLanguage(i18n.language) ?? "en";

  async function changeLanguage(value: string) {
    const language = normalizeLanguage(value);
    if (!language) return;
    try {
      localStorage.setItem("hi-language", language);
    } catch {
      // The selection still applies for this session if storage is unavailable.
    }
    await i18n.changeLanguage(language);
  }

  return (
    <div className={cn("flex min-w-0 items-center gap-2", className)}>
      <Globe2 className="size-4 shrink-0" aria-hidden="true" />
      <Select value={currentLanguage} onValueChange={(value) => void changeLanguage(value)}>
        <SelectTrigger className="w-full min-w-0" aria-label={t("language.label")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          {languages.map((language) => (
            <SelectItem key={language} value={language}>
              {languageNames[language]}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
