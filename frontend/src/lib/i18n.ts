import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import en from "@/locales/en.json";
import zhCN from "@/locales/zh-CN.json";
import es from "@/locales/es.json";
import ja from "@/locales/ja.json";
import ptBR from "@/locales/pt-BR.json";
import de from "@/locales/de.json";
import fr from "@/locales/fr.json";
import zhTW from "@/locales/zh-TW.json";
import ru from "@/locales/ru.json";
import ko from "@/locales/ko.json";
import id from "@/locales/id.json";
import it from "@/locales/it.json";
import pl from "@/locales/pl.json";
import tr from "@/locales/tr.json";

export const languages = ["en", "zh-CN", "es", "ja", "pt-BR", "de", "fr", "zh-TW", "ru", "ko", "id", "it", "pl", "tr"] as const;
export type Language = (typeof languages)[number];

export const languageNames: Record<Language, string> = {
  en: "English",
  "zh-CN": "简体中文",
  es: "Español",
  ja: "日本語",
  "pt-BR": "Português (Brasil)",
  de: "Deutsch",
  fr: "Français",
  "zh-TW": "繁體中文",
  ru: "Русский",
  ko: "한국어",
  id: "Bahasa Indonesia",
  it: "Italiano",
  pl: "Polski",
  tr: "Türkçe",
};

const resources = {
  en: { translation: en },
  "zh-CN": { translation: zhCN },
  es: { translation: es },
  ja: { translation: ja },
  "pt-BR": { translation: ptBR },
  de: { translation: de },
  fr: { translation: fr },
  "zh-TW": { translation: zhTW },
  ru: { translation: ru },
  ko: { translation: ko },
  id: { translation: id },
  it: { translation: it },
  pl: { translation: pl },
  tr: { translation: tr },
} as const;

const LANGUAGE_STORAGE_KEY = "hi-language";

export function normalizeLanguage(value: string | undefined): Language | undefined {
  if (!value) return undefined;
  const normalized = value.replaceAll("_", "-").toLowerCase();
  if (normalized.startsWith("zh-tw") || normalized.startsWith("zh-hk") || normalized.startsWith("zh-mo") || normalized.includes("hant")) return "zh-TW";
  if (normalized.startsWith("zh") && !normalized.includes("hant")) return "zh-CN";
  if (normalized.startsWith("pt")) return "pt-BR";
  const base = normalized.split("-")[0];
  return languages.find((language) => language.toLowerCase() === base);
}

function initialLanguage(): Language {
  try {
    const saved = normalizeLanguage(localStorage.getItem(LANGUAGE_STORAGE_KEY) ?? undefined);
    if (saved) return saved;
  } catch {
    // Storage may be unavailable in private browsing; browser preferences still work.
  }
  const preferences = navigator.languages?.length ? navigator.languages : [navigator.language];
  for (const preference of preferences) {
    const language = normalizeLanguage(preference);
    if (language) return language;
  }
  return "en";
}

void i18n.use(initReactI18next).init({
  resources,
  lng: initialLanguage(),
  fallbackLng: "en",
  interpolation: { escapeValue: false },
});

function syncDocumentLanguage(language: string) {
  document.documentElement.lang = normalizeLanguage(language) ?? "en";
}

i18n.on("languageChanged", syncDocumentLanguage);
syncDocumentLanguage(i18n.language);

export default i18n;
