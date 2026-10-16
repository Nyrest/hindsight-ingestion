import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";

const directory = new URL("../src/locales/", import.meta.url);
for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  test(`document deletion progress resolves and interpolates in ${file}`, async () => {
    const language = file.slice(0, -5);
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    const prefix = "tasks.clearDocuments.progress.";
    for (const key of ["label", "preparing", "deleting", "finalizing", "estimating", "unavailable"]) {
      expect(i18n.exists(prefix + key)).toBe(true);
      expect(i18n.t(prefix + key).trim()).not.toBe("");
    }
    const counts = i18n.t(prefix + "count", { completed: "2", total: "8", deleted: "1" });
    for (const number of ["2", "8", "1"]) expect(counts).toContain(number);
    expect(counts).not.toContain("{{");
    expect(i18n.t(prefix + "eta", { time: "10s" })).toContain("10s");
  });
}
