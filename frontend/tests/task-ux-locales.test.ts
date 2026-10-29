import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";

const directory = new URL("../src/locales/", import.meta.url);
for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  test(`task UX copy resolves and interpolates in ${file}`, async () => {
    const language = file.slice(0, -5);
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    for (const key of ["tasks.actions.runAll", "tasks.runAll.result", "credentials.oauth.tokenRefresh", "settings.oauth.redirectUri", "dashboard.failures.title", "dashboard.failures.empty"]) {
      expect(i18n.exists(key)).toBe(true);
      const copy = i18n.t(key, { started: 3, failed: 2, skipped: 1, when: "TIME" });
      expect(copy).not.toBe(key);
      expect(copy).not.toContain("{{");
    }
    expect(i18n.t("credentials.oauth.tokenRefresh", { when: "TIME" })).toContain("TIME");
    const result = i18n.t("tasks.runAll.result", { started: 3, failed: 2, skipped: 1 });
    for (const number of ["3", "2", "1"]) expect(result).toContain(number);
  });
}
