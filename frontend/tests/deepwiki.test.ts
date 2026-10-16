import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { createInstance } from "i18next";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { SourceConfigFields } from "../src/features/tasks/source-section";
import { emptyTaskForm, taskFormSchema } from "../src/features/tasks/task-form-model";
import { providerCopy } from "../src/lib/provider-copy";
import type { SourceSpec } from "../src/lib/types";

const wiki = {
  type: "deepwiki", name: "DeepWiki", credentialTypes: [], browseKinds: [],
  capabilities: {}, fields: [{ key: "repoName", label: "Repository name (case-sensitive)", type: "string", required: true, secret: false }],
  filterFields: [], filterCapabilities: { combinators: ["and", "or"], maxDepth: 8 },
} as SourceSpec;

test("DeepWiki tasks accept an empty credential while other sources still require one", () => {
  const values = { ...emptyTaskForm(), name: "Wiki", sourceType: "deepwiki", sourceConfig: { repoName: "Owner/Repo" }, destinationCredentialId: "hindsight", destinationBankId: "bank" };
  const schema = taskFormSchema([wiki, { ...wiki, type: "notion", credentialTypes: ["notion"] }]);
  expect(schema.safeParse(values).success).toBe(true);
  const result = schema.safeParse({ ...values, sourceType: "notion" });
  expect(result.success).toBe(false);
  if (!result.success) expect(result.error.issues.some((issue) => issue.path[0] === "sourceCredentialId")).toBe(true);
});

test("DeepWiki shows a repository field without a credential picker", async () => {
  const i18n = createInstance();
  const translation = JSON.parse(readFileSync(new URL("../src/locales/en.json", import.meta.url), "utf8"));
  await i18n.init({ lng: "en", resources: { en: { translation } } });
  const fields = createElement(SourceConfigFields, {
    source: wiki, credentials: [], credentialId: "", onCredentialChange: () => {},
    config: { repoName: "Owner/Repo" }, onConfigChange: () => {}, configErrors: {}, editing: false,
  });
  const markup = renderToStaticMarkup(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(I18nextProvider, { i18n }, fields)));
  expect(markup).toContain('value="Owner/Repo"');
  expect(markup).toContain("Repository name (case-sensitive)");
  expect(markup).not.toContain("task-source-cred");
});

for (const file of readdirSync(new URL("../src/locales/", import.meta.url)).filter((name) => name.endsWith(".json"))) {
  test(`DeepWiki case sensitivity label resolves in ${file}`, async () => {
    const language = file.slice(0, -5);
    const translation = JSON.parse(readFileSync(new URL(`../src/locales/${file}`, import.meta.url), "utf8"));
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    const label = providerCopy("Repository name (case-sensitive)", i18n.t.bind(i18n));
    expect(label).toBe(translation.providerCopy.deepwikiRepoName);
    expect(label.trim()).not.toBe("");
    if (language !== "en") expect(label).not.toBe("Repository name (case-sensitive)");
  });
}
