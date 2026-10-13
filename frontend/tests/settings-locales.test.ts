import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";

const directory = new URL("../src/locales/", import.meta.url);
const keys = ["settings.sync.maxConcurrentTasks", "settings.sync.maxConcurrentTasksHelp"];

for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  const language = file.slice(0, -5);
  test(`concurrency settings resolve to translated copy in ${language}`, async () => {
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    for (const key of keys) {
      expect(i18n.exists(key)).toBe(true);
      expect(i18n.t(key)).not.toBe(key);
      expect(i18n.t(key).trim()).not.toBe("");
    }
    if (language === "zh-CN") {
      expect(i18n.t(keys[0])).toBe("最大并发任务数");
    }
  });
}
