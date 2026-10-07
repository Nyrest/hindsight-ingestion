import type { KeyValueRow } from "@/components/key-value-editor";

export type HeadersParseResult =
  | { ok: true; headers: Record<string, string> }
  | { ok: false; error: string; errorKey: "invalidJson" | "notObject" | "nonString" | "duplicate" | "emptyName"; detail?: string };

export function rowsFromHeaders(headers: Record<string, string> | null | undefined): KeyValueRow[] {
  return Object.entries(headers ?? {}).map(([key, value]) => ({ key, value: String(value ?? "") }));
}

/** Serialize UI rows to JSON text (blank rows are dropped). Preserves order and masked values. */
export function rowsToJson(rows: KeyValueRow[]): string {
  const obj: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (!k) continue;
    obj[k] = r.value;
  }
  return Object.keys(obj).length ? JSON.stringify(obj, null, 2) : "{}";
}

/** Find the first case-insensitive duplicate header name in a list of names. */
function firstDuplicate(names: string[]): string | null {
  const seen = new Set<string>();
  for (const n of names) {
    const k = n.trim().toLowerCase();
    if (!k) continue;
    if (seen.has(k)) return n.trim();
    seen.add(k);
  }
  return null;
}

/**
 * Parse JSON text. JSON.parse silently merges duplicate keys, so duplicate detection
 * is done by scanning the raw top-level keys with a tokenizer.
 */
export function parseHeadersJson(text: string): HeadersParseResult {
  const trimmed = text.trim();
  if (!trimmed) return { ok: true, headers: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (e) {
    return { ok: false, errorKey: "invalidJson", error: "invalid JSON", detail: e instanceof Error ? e.message : undefined };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { ok: false, errorKey: "notObject", error: "not an object" };
  }
  const rawKeys = topLevelKeys(trimmed);
  const dup = firstDuplicate(rawKeys);
  if (dup) return { ok: false, errorKey: "duplicate", error: "duplicate", detail: dup };

  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
    if (!k.trim()) return { ok: false, errorKey: "emptyName", error: "empty name" };
    if (typeof v !== "string") return { ok: false, errorKey: "nonString", error: "non-string", detail: k };
    headers[k.trim()] = v;
  }
  return { ok: true, headers };
}

/** Validate UI rows: returns headers or an error (duplicate / empty name with value). */
export function headersFromRows(rows: KeyValueRow[]): HeadersParseResult {
  const names = rows.map((r) => r.key).filter((k) => k.trim());
  const dup = firstDuplicate(names);
  if (dup) return { ok: false, errorKey: "duplicate", error: "duplicate", detail: dup };
  for (const r of rows) {
    if (!r.key.trim() && r.value.trim()) return { ok: false, errorKey: "emptyName", error: "empty name" };
  }
  const headers: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) headers[k] = r.value;
  }
  return { ok: true, headers };
}

/** Extract top-level object keys (in order, including duplicates) from JSON text known to be valid. */
function topLevelKeys(text: string): string[] {
  const keys: string[] = [];
  let depth = 0;
  let i = 0;
  let expectKey = false;
  while (i < text.length) {
    const ch = text[i];
    if (ch === '"') {
      let j = i + 1;
      let s = "";
      while (j < text.length && text[j] !== '"') {
        if (text[j] === "\\") {
          s += text.slice(j, j + 2);
          j += 2;
        } else {
          s += text[j];
          j++;
        }
      }
      if (depth === 1 && expectKey) {
        try {
          keys.push(JSON.parse(`"${s}"`) as string);
        } catch {
          keys.push(s);
        }
        expectKey = false;
      }
      i = j + 1;
      continue;
    }
    if (ch === "{" || ch === "[") {
      depth++;
      if (depth === 1 && ch === "{") expectKey = true;
    } else if (ch === "}" || ch === "]") {
      depth--;
    } else if (ch === "," && depth === 1) {
      expectKey = true;
    }
    i++;
  }
  return keys;
}
