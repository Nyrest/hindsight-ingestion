import { expect, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nextProvider } from "react-i18next";
import { createInstance } from "i18next";
import { readdirSync, readFileSync } from "node:fs";
import { GitSourceFields } from "../src/features/tasks/git-source-fields";
import { emptyTaskForm, taskFormSchema } from "../src/features/tasks/task-form-model";
import { providerCopy } from "../src/lib/provider-copy";
import type { SourceSpec, FieldSpec } from "../src/lib/types";

const directory = new URL("../src/locales/", import.meta.url);
const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
const keys = Object.keys(english.providerCopy).filter((key) => key.startsWith("git"));

test("Git credentials are optional while required-source validation remains intact", () => {
  const git = { type: "git", credentialTypes: ["git"], credentialOptional: true } as SourceSpec;
  const sftp = { type: "sftp", credentialTypes: ["sftp"], credentialOptional: false } as SourceSpec;
  const values = { ...emptyTaskForm(), name: "Git", sourceType: "git", destinationCredentialId: "hindsight", destinationBankId: "bank" };
  const schema = taskFormSchema([git, sftp]);
  expect(schema.safeParse(values).success).toBe(true);
  expect(schema.safeParse({ ...values, sourceCredentialId: "git-credential" }).success).toBe(true);
  expect(schema.safeParse({ ...values, sourceType: "sftp" }).success).toBe(false);
});

test("Git reference form shows only the fields used by the selected reference type", async () => {
  const i18n = createInstance();
  await i18n.init({ lng: "en", resources: { en: { translation: english } } });
  const fields = [
    { key: "repositoryUrl", label: "Repository URL", type: "string" },
    { key: "ref", label: "Branch / tag / commit", type: "string" },
    { key: "tagPattern", label: "Tag pattern", type: "string" },
  ] as FieldSpec[];
  const render = (refType: string) => renderToStaticMarkup(createElement(I18nextProvider, { i18n }, createElement(GitSourceFields, {
    fields, config: { refType }, onChange: () => {}, errors: {}, editing: false,
  })));
  expect(render("branch")).toContain('id="src-ref"');
  expect(render("branch")).not.toContain('id="src-tagPattern"');
  expect(render("latest_tag")).toContain('id="src-tagPattern"');
  expect(render("latest_tag")).not.toContain('id="src-ref"');
});

for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
  test(`Git fields and anonymous access resolve in ${file}`, async () => {
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    const language = file.slice(0, -5);
    const i18n = createInstance();
    await i18n.init({ lng: language, fallbackLng: false, resources: { [language]: { translation } } });
    expect(keys.length).toBe(19);
    for (const key of keys) {
      expect(i18n.exists(`providerCopy.${key}`)).toBe(true);
      expect(providerCopy(english.providerCopy[key], i18n.t.bind(i18n))).toBe(translation.providerCopy[key]);
    }
    expect(i18n.exists("source.anonymous")).toBe(true);
    if (language !== "en") expect(translation.providerCopy.gitDescription).not.toBe(english.providerCopy.gitDescription);
  });
}
