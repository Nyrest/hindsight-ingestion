// Loaded lazily: importing this module pulls in Monaco and its workers.
import * as monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import TsWorker from "monaco-editor/language/typescript/ts.worker.js?worker";
import { useEffect, useRef } from "react";
import { useTheme } from "@/lib/theme";
import { RUNTIME_TYPINGS } from "./typings";

self.MonacoEnvironment = {
  getWorker(_id: string, label: string) {
    return label === "typescript" || label === "javascript" ? new TsWorker() : new EditorWorker();
  },
};

const js = monaco.typescript.javascriptDefaults;
js.setCompilerOptions({
  target: monaco.typescript.ScriptTarget.ES2020,
  lib: ["es2020"],
  allowJs: true,
  checkJs: true,
  allowNonTsExtensions: true,
  noEmit: true,
  strict: false,
});
js.setDiagnosticsOptions({ noSemanticValidation: false, noSyntaxValidation: false });
js.setEagerModelSync(true);
js.addExtraLib(RUNTIME_TYPINGS, "file:///post-processing-runtime.d.ts");

export interface CodeEditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Accessible label of the editing area. */
  ariaLabel?: string;
  /** Model URI path; one per editor instance. */
  path: string;
  height?: number;
}

export default function CodeEditor({ value, onChange, ariaLabel, path, height = 420 }: CodeEditorProps) {
  const { resolvedTheme } = useTheme();
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  useEffect(() => {
    if (!host.current) return;
    const uri = monaco.Uri.parse(`file:///${path}.js`);
    const model = monaco.editor.getModel(uri) ?? monaco.editor.createModel(value, "javascript", uri);
    if (model.getValue() !== value) model.setValue(value);
    const instance = monaco.editor.create(host.current, {
      model,
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      tabSize: 2,
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: true,
      formatOnPaste: true,
      ariaLabel,
      padding: { top: 8, bottom: 8 },
      theme: document.documentElement.classList.contains("dark") ? "vs-dark" : "vs",
    });
    editor.current = instance;
    const sub = model.onDidChangeContent(() => onChangeRef.current(model.getValue()));
    return () => {
      sub.dispose();
      instance.dispose();
      model.dispose();
      editor.current = null;
    };
    // The editor owns its model after mount; external value changes are synced below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  useEffect(() => {
    const model = editor.current?.getModel();
    if (model && model.getValue() !== value) {
      // Keeps undo history, unlike setValue.
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text: value }], () => null);
    }
  }, [value]);

  useEffect(() => {
    monaco.editor.setTheme(resolvedTheme === "dark" ? "vs-dark" : "vs");
  }, [resolvedTheme]);

  return <div ref={host} className="overflow-hidden rounded-md border" style={{ height }} />;
}
