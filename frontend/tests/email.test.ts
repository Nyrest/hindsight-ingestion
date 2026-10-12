import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { normalizeConfig } from "../src/components/schema-field";
import { providerCopy } from "../src/lib/provider-copy";
import type { FieldSpec } from "../src/lib/types";

test("Email and IMAP copy exists in every locale", () => {
  const directory = new URL("../src/locales/", import.meta.url);
  const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
  const keys = Object.keys(english.providerCopy).filter((key) => key.startsWith("email") || key.startsWith("imap"));
  expect(keys.length).toBe(17);
  for (const name of readdirSync(directory)) {
    const locale = JSON.parse(readFileSync(new URL(name, directory), "utf8"));
    for (const key of keys) expect(locale.providerCopy[key]).toBeTruthy();
  }
  expect(providerCopy("Mail folders", (key) => key)).toBe("providerCopy.emailFolders");
  expect(providerCopy("Email", (key) => key)).toBe("providerCopy.emailName");
});

test("Browsed mailbox names preserve server whitespace", () => {
  const fields = [{ key: "folders", type: "string_list", browse: true }] as FieldSpec[];
  expect(normalizeConfig(fields, { folders: "INBOX\n Mailbox with spaces \n\n" })).toEqual({ folders: ["INBOX", " Mailbox with spaces "] });
});
