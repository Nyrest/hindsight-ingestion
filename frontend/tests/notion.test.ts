import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";
import { defaultConfig, mergeConfig, normalizeConfig } from "../src/components/schema-field";
import { sourceGroupTags } from "../src/features/settings/config-model";
import { validateNotionConfig } from "../src/features/tasks/notion-source-model";
import { providerCopy } from "../src/lib/provider-copy";
import type { FieldSpec } from "../src/lib/types";

const fields: FieldSpec[] = [
  { key: "mode", label: "Source mode", type: "select", required: false, secret: false, default: "database" },
  { key: "dataSourceId", label: "Data source", type: "string", required: false, secret: false, browse: true },
  { key: "pageIds", label: "Pages", type: "string_list", required: false, secret: false, browse: true },
  { key: "includeSubpages", label: "Include subpages", type: "boolean", required: false, secret: false, default: false },
  { key: "maxDepth", label: "Maximum traversal depth", type: "number", required: false, secret: false, default: 3, nullable: true },
];
const t = (key: string) => key;

test("old Notion configuration keeps its database scope", () => {
  const config = mergeConfig(fields, { dataSourceId: "ds" });
  expect(config).toMatchObject({ mode: "database", dataSourceId: "ds", includeSubpages: false, maxDepth: 3 });
  expect(validateNotionConfig(fields, config, t)).toEqual({});
  expect(sourceGroupTags("notion", config)).toEqual(["notion_data_source_id:ds"]);
  expect(defaultConfig(fields).includeSubpages).toBe(false);
});

test("pages mode validates only the active source and normalizes multiple IDs", () => {
  const config = { ...defaultConfig(fields), mode: "pages", pageIds: "first\nsecond" };
  expect(validateNotionConfig(fields, config, t)).toEqual({});
  expect(normalizeConfig(fields, config).pageIds).toEqual(["first", "second"]);
  expect(validateNotionConfig(fields, { ...config, pageIds: [] }, t)).toEqual({ pageIds: "validation.required" });
  expect(validateNotionConfig(fields, { ...config, mode: "database" }, t)).toEqual({ dataSourceId: "validation.required" });
  expect(sourceGroupTags("notion", { ...config, dataSourceId: "inactive" })).toEqual(["notion_root_page_id:first", "notion_root_page_id:second"]);
});

test("unlimited depth survives merge and submission; numeric boundaries are validated", () => {
  const config = mergeConfig(fields, { mode: "pages", pageIds: ["root"], includeSubpages: true, maxDepth: null });
  expect(normalizeConfig(fields, config).maxDepth).toBeNull();
  expect(validateNotionConfig(fields, config, t)).toEqual({});
  expect(normalizeConfig(fields, { ...config, maxDepth: "0" }).maxDepth).toBe(0);
  for (const maxDepth of [-1, 1.5, Infinity, 2147483648]) {
    expect(validateNotionConfig(fields, { ...config, maxDepth }, t).maxDepth).toBe("notion.depthError");
  }
});

const directory = new URL("../src/locales/", import.meta.url);
const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
const providerKeys = ["notionDescription", "notionPages", "notionPagesHelp", "notionIncludeSubpages", "notionIncludeSubpagesHelp", "notionMaxDepth", "notionDepthHelp"];
for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  test(`Notion options resolve in ${file}`, async () => {
    const language = file.slice(0, -5);
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    for (const key of Object.keys(english.notion)) {
      expect(i18n.exists(`notion.${key}`)).toBe(true);
      expect(i18n.t(`notion.${key}`).trim()).not.toBe("");
    }
    for (const key of providerKeys) {
      expect(i18n.exists(`providerCopy.${key}`)).toBe(true);
      expect(providerCopy(english.providerCopy[key], i18n.t.bind(i18n))).toBe(translation.providerCopy[key]);
    }
  });
}
