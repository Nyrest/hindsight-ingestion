import { describe, expect, test } from "bun:test";
import { defaultConfig, mergeConfig, normalizeConfig, validateConfig } from "../src/components/schema-field";
import type { FieldSpec } from "../src/lib/types";

const fields: FieldSpec[] = [
  { key: "includePatterns", label: "Include", type: "string_list", required: false, secret: false, default: [] },
  { key: "rejectResourceTypes", label: "Block", type: "multi_select", required: false, secret: false, default: ["image", "font"], options: [{ value: "image", label: "Image" }, { value: "font", label: "Font" }], renderOnly: true },
  { key: "navigationTimeout", label: "Timeout", type: "number", required: false, secret: false, default: 60000, renderOnly: true },
];
const t = (key: string) => key;

describe("list configuration", () => {
  test("defaults and saved arrays survive loading and serialization", () => {
    expect(defaultConfig(fields)).toEqual({ includePatterns: [], rejectResourceTypes: ["image", "font"], navigationTimeout: 60000 });
    const config = mergeConfig(fields, { render: true, includePatterns: ["https://example.com/**"], rejectResourceTypes: [] });
    expect(normalizeConfig(fields, config).rejectResourceTypes).toEqual([]);
    expect(validateConfig(fields, config, t)).toEqual({});
    expect(normalizeConfig(fields, { includePatterns: " https://example.com/docs/** \r\n\nhttps://example.com/help/* " }).includePatterns)
      .toEqual(["https://example.com/docs/**", "https://example.com/help/*"]);
  });
  test("validates arrays and choices while ignoring browser-only values for static crawls", () => {
    expect(validateConfig(fields, { render: true, includePatterns: [42], rejectResourceTypes: ["unknown"] }, t))
      .toEqual({ includePatterns: "validation.list", rejectResourceTypes: "validation.list" });
    expect(validateConfig(fields, { render: false, rejectResourceTypes: ["unknown"], navigationTimeout: "invalid" }, t)).toEqual({});
  });
});
