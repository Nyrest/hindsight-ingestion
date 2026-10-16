import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";
import { providerCopy } from "../src/lib/provider-copy";

const directory = new URL("../src/locales/", import.meta.url);
const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
const keys = Object.keys(english.providerCopy).filter((key) => key.startsWith("steam"));

for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  const language = file.slice(0, -5);
  test(`Steam options resolve to translated copy in ${language}`, async () => {
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    for (const key of keys) {
      const path = `providerCopy.${key}`;
      expect(i18n.exists(path)).toBe(true);
      expect(i18n.t(path)).not.toBe(path);
      expect(i18n.t(path).trim()).not.toBe("");
    }
    for (const key of ["steamIncludeAchievements", "steamIncludeAchievementsHelp"]) {
      expect(providerCopy(english.providerCopy[key], i18n.t.bind(i18n))).toBe(translation.providerCopy[key]);
      if (language !== "en") expect(translation.providerCopy[key]).not.toBe(english.providerCopy[key]);
    }
    if (language === "zh-CN") {
      expect(i18n.t("providerCopy.steamIncludeAchievements")).toBe("包含成就详情");
    }
  });
}
