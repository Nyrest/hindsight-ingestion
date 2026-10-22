import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import ts from "typescript";
import { DEFAULT_CODE, PROCESS_DOCUMENTATION, STEP_TYPES, firstInvalidStep, generateCode, newStep, stepErrors } from "../src/features/tasks/post-processing/model";
import { RUNTIME_TYPINGS } from "../src/features/tasks/post-processing/typings";

function codeProgram(code: string) {
  const filename = path.resolve("post-processing-editor.js").replaceAll("\\", "/");
  const definitions = path.resolve("post-processing-runtime.d.ts").replaceAll("\\", "/");
  const options: ts.CompilerOptions = {
    allowJs: true, checkJs: true, noEmit: true, types: [],
    target: ts.ScriptTarget.ES2020, lib: ["lib.es2020.d.ts"],
  };
  const host = ts.createCompilerHost(options);
  const getSourceFile = host.getSourceFile.bind(host);
  host.getSourceFile = (name, version, onError, createNew) => {
    if (name === filename) return ts.createSourceFile(name, code, version, true, ts.ScriptKind.JS);
    if (name === definitions) return ts.createSourceFile(name, RUNTIME_TYPINGS, version, true, ts.ScriptKind.TS);
    return getSourceFile(name, version, onError, createNew);
  };
  const program = ts.createProgram([filename, definitions], options, host);
  return { program, source: program.getSourceFile(filename)! };
}

test("editor comments describe complete parameter and return types", () => {
  const generated = generateCode([newStep("trim"), { ...newStep("skip"), pattern: "draft" }]);
  for (const code of [DEFAULT_CODE, generated]) {
    const { program, source } = codeProgram(code);
    expect(ts.getPreEmitDiagnostics(program).map((d) => ts.flattenDiagnosticMessageText(d.messageText, "\n"))).toEqual([]);
    const checker = program.getTypeChecker();
    const process = source.statements.find(ts.isFunctionDeclaration)!;
    const contextType = checker.getTypeAtLocation(process.parameters[0]);
    const contextDeclaration = program.getSourceFiles().flatMap((file) => [...file.statements])
      .find((statement) => ts.isInterfaceDeclaration(statement) && statement.name.text === "ProcessContext")!;
    const declaredContext = checker.getTypeAtLocation(contextDeclaration);
    expect(checker.isTypeAssignableTo(contextType, declaredContext)).toBe(true);
    expect(checker.getPropertiesOfType(contextType).map((p) => p.name).sort()).toEqual(["document", "item", "run", "task"]);
    expect(checker.typeToString(checker.getTypeAtLocation(process.parameters[1]))).toBe("string | ArrayBuffer");
    const signature = checker.getSignatureFromDeclaration(process)!;
    const returned = checker.typeToString(checker.getReturnTypeOfSignature(signature));
    expect(returned).toContain("Promise<");
    expect(returned).toContain("ArrayBufferView");
    expect(returned).toContain("fileName?: string");
  }
});

test("backend and frontend share identical editor documentation", () => {
  const backend = readFileSync(new URL("../../backend/internal/postprocess/config.go", import.meta.url), "utf8").replaceAll("\r\n", "\n");
  const documentation = backend.match(/const ProcessDocumentation = `([\s\S]*?)`/);
  expect(documentation?.[1]).toBe(PROCESS_DOCUMENTATION);
});

test("disabled steps remain commented and preserve line separators safely", () => {
  const disabled = { ...newStep("prepend"), enabled: false, text: "before\u2028middle\u2029after" };
  const code = generateCode([disabled, newStep("trim")]);
  expect(code).toContain('  // input = await steps.prepend(context, input, {"separator":"\\n\\n","text":"before\\u2028middle\\u2029after"});');
  expect(code.indexOf("// input = await steps.prepend")).toBeLessThan(code.indexOf("  input = await steps.trim"));
  expect(ts.getPreEmitDiagnostics(codeProgram(code).program)).toEqual([]);
});

test("generated code matches the backend generator", () => {
  // Same expectation as TestGenerate in backend/internal/postprocess.
  const code = generateCode([
    { id: "1", type: "collapse_whitespace", enabled: true },
    { id: "2", type: "replace", enabled: true, pattern: "<a>", replacement: "b" },
    { id: "3", type: "trim", enabled: false, side: "both" },
  ]);
  expect(code).toBe(
    "// Generated from post-processing steps.\n" + PROCESS_DOCUMENTATION + "async function process(context, input) {\n" +
      "  input = await steps.collapseWhitespace(context, input, {});\n" +
      '  input = await steps.replace(context, input, {"pattern":"<a>","replacement":"b"});\n' +
      '  // input = await steps.trim(context, input, {"side":"both"});\n' +
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
