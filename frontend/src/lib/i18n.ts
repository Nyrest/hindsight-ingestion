import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import en from "@/locales/en.json";

// To add a language: create src/locales/<lng>.json with the same structure and register it here.
const resources = {
  en: { translation: en },
} as const;

void i18n.use(initReactI18next).init({
  resources,
  lng: "en",
  fallbackLng: "en",
  interpolation: { escapeValue: false },
});

export default i18n;
