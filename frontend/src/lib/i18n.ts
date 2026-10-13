import i18n, { type BackendModule, type ResourceKey } from "i18next";
import { initReactI18next } from "react-i18next";
import en from "@/locales/en.json";

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

const localeLoaders = import.meta.glob<ResourceKey>("../locales/*.json", { import: "default" });
const localeBackend: BackendModule = {
  type: "backend",
  init() {},
  read(language, _namespace, callback) {
    const load = localeLoaders[`../locales/${language}.json`];
    if (!load) {
      callback(new Error(`Unsupported language: ${language}`), false);
      return;
    }
    void load().then((translation) => callback(null, translation), (error: Error) => callback(error, false));
  },
};

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

export const i18nReady = i18n.use(localeBackend).use(initReactI18next).init({
  resources: { en: { translation: en } },
  partialBundledLanguages: true,
  supportedLngs: languages,
  load: "currentOnly",
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
