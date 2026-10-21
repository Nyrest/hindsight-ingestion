import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";
import { providerCopy } from "../src/lib/provider-copy";

const directory = new URL("../src/locales/", import.meta.url);
const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
const keys = Object.keys(english.providerCopy).filter((key) => key.startsWith("caldav"));

for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  const language = file.slice(0, -5);
  test(`CalDAV fields resolve in ${language}`, async () => {
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    expect(keys.length).toBe(5);
    for (const key of keys) {
      expect(i18n.exists(`providerCopy.${key}`)).toBe(true);
      expect(providerCopy(english.providerCopy[key], i18n.t.bind(i18n))).toBe(translation.providerCopy[key]);
      if (language !== "en") expect(translation.providerCopy[key]).not.toBe(english.providerCopy[key]);
    }
  });
}
