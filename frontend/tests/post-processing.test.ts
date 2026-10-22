import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { STEP_TYPES, firstInvalidStep, generateCode, newStep, stepErrors } from "../src/features/tasks/post-processing/model";

test("generated code matches the backend generator", () => {
  // Same expectation as TestGenerate in backend/internal/postprocess.
  const code = generateCode([
    { id: "1", type: "collapse_whitespace", enabled: true },
    { id: "2", type: "replace", enabled: true, pattern: "<a>", replacement: "b" },
    { id: "3", type: "trim", enabled: false, side: "both" },
  ]);
  expect(code).toBe(
    "// Generated from post-processing steps.\nasync function process(context, input) {\n" +
      "  input = await steps.collapseWhitespace(context, input, {});\n" +
      '  input = await steps.replace(context, input, {"pattern":"<a>","replacement":"b"});\n' +
      "  return input;\n}\n",
  );
});

test("new steps of every type are valid once required fields are set", () => {
  for (const type of STEP_TYPES) {
    const step = { ...newStep(type), pattern: "x", url: "https://example.com" };
    expect(stepErrors(step)).toEqual({});
  }
  expect(stepErrors({ ...newStep("replace"), pattern: "(", regex: true })).toEqual({ pattern: "postProcessing.errors.regex" });
  expect(firstInvalidStep({ enabled: false, mode: "code", code: "", steps: [newStep("trim"), newStep("http")] })).toBe(1);
});

test("every locale translates post-processing copy", () => {
  const directory = new URL("../src/locales/", import.meta.url);
  const flatten = (o: Record<string, unknown>, prefix = ""): string[] =>
    Object.entries(o).flatMap(([k, v]) => (v && typeof v === "object" ? flatten(v as Record<string, unknown>, `${prefix}${k}.`) : [`${prefix}${k}`]));
  const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
  const keys = flatten(english.postProcessing);
  for (const type of STEP_TYPES) expect(keys).toContain(`steps.${type}.title`);
  for (const file of readdirSync(directory).filter((name) => name.endsWith(".json"))) {
    const translation = JSON.parse(readFileSync(new URL(file, directory), "utf8"));
    expect(flatten(translation.postProcessing).sort()).toEqual([...keys].sort());
    expect(translation.taskEditor.sections.postprocess.title.trim()).not.toBe("");
  }
});
