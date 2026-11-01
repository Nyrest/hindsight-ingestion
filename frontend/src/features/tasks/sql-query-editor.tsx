// Loaded only when a SQL task is opened. Suggestions are entirely local.
import { useEffect, useRef } from "react";
import { monaco } from "@/lib/monaco";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const keywords = [
  "SELECT", "DISTINCT", "FROM", "WHERE", "AS", "AND", "OR", "NOT", "NULL",
  "IS NULL", "IS NOT NULL", "IN", "EXISTS", "BETWEEN", "LIKE", "JOIN",
  "INNER JOIN", "LEFT JOIN", "RIGHT JOIN", "CROSS JOIN", "ON", "GROUP BY",
  "HAVING", "ORDER BY", "ASC", "DESC", "LIMIT", "OFFSET", "UNION", "UNION ALL",
  "WITH", "CASE", "WHEN", "THEN", "ELSE", "END", "OVER", "PARTITION BY",
];
const functions = ["COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "NULLIF", "CAST", "CONCAT", "LOWER", "UPPER", "TRIM", "LENGTH", "ROUND"];

// Register once per module, rather than once per editor mount.
monaco.languages.registerCompletionItemProvider("sql", {
  provideCompletionItems(model, position) {
    const word = model.getWordUntilPosition(position);
    const range = new monaco.Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn);
    return {
      suggestions: [
        ...keywords.map((label) => ({ label, kind: monaco.languages.CompletionItemKind.Keyword, insertText: label, range })),
        ...functions.map((label) => ({
          label,
          kind: monaco.languages.CompletionItemKind.Function,
          insertText: `${label}(\${1})`,
          insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
          range,
        })),
        ...["__id", "__content", "__context"].map((label) => ({ label, kind: monaco.languages.CompletionItemKind.Field, insertText: label, range })),
        {
          label: "SELECT rows",
          kind: monaco.languages.CompletionItemKind.Snippet,
          insertText: "SELECT ${1:id} AS __id, ${2:body} AS __content,\n       ${3:context} AS __context\nFROM ${4:table_name}",
          insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
          range,
        },
        {
          label: "SELECT query",
          kind: monaco.languages.CompletionItemKind.Snippet,
          insertText: "SELECT ${1:*}\nFROM ${2:table_name}\nWHERE ${3:condition}",
          insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
          range,
        },
      ],
    };
  },
});

export default function SQLQueryEditor({ value, onChange, ariaLabel, invalid }: {
  value: string;
  onChange: (value: string) => void;
  ariaLabel: string;
  invalid: boolean;
}) {
  const { resolvedTheme } = useTheme();
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const initialValue = useRef(value);

  useEffect(() => {
    if (!host.current) return;
    const model = monaco.editor.createModel(initialValue.current, "sql");
    const instance = monaco.editor.create(host.current, {
      model,
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      // Use the textarea input so the form label and validation attributes apply.
      editContext: false,
      tabSize: 2,
      tabFocusMode: true,
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: true,
      wordWrap: "on",
      quickSuggestions: { other: true, comments: false, strings: false },
      wordBasedSuggestions: "currentDocument",
      padding: { top: 10, bottom: 10 },
      theme: document.documentElement.classList.contains("dark") ? "vs-dark" : "vs",
    });
    editor.current = instance;
    const subscription = model.onDidChangeContent(() => onChangeRef.current(model.getValue()));
    return () => {
      subscription.dispose();
      instance.dispose();
      model.dispose();
      editor.current = null;
    };
  }, []);

  useEffect(() => {
    const model = editor.current?.getModel();
    if (model && model.getValue() !== value) {
      model.pushEditOperations([], [{ range: model.getFullModelRange(), text: value }], () => null);
    }
  }, [value]);

  useEffect(() => {
    editor.current?.updateOptions({ ariaLabel, ariaRequired: true });
    // Keep the form label and validation state on Monaco's actual editing control.
    const input = host.current?.querySelector("textarea");
    input?.setAttribute("id", "sql-query");
    input?.setAttribute("aria-invalid", String(invalid));
  }, [ariaLabel, invalid]);

  useEffect(() => {
    monaco.editor.setTheme(resolvedTheme === "dark" ? "vs-dark" : "vs");
  }, [resolvedTheme]);

  return <div ref={host} className={cn("h-64 min-w-0 overflow-hidden rounded-md border focus-within:ring-2 focus-within:ring-ring", invalid && "border-destructive")} />;
}
