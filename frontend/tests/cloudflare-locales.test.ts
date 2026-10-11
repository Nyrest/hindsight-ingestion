import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { providerCopy } from "../src/lib/provider-copy";

test("Cloudflare copy and waiting status exist in every locale", () => {
  const directory = new URL("../src/locales/", import.meta.url);
  const english = JSON.parse(readFileSync(new URL("en.json", directory), "utf8"));
  const copyKeys = Object.keys(english.providerCopy).filter((key) => key.startsWith("cloudflare"));
  for (const name of readdirSync(directory)) {
    const locale = JSON.parse(readFileSync(new URL(name, directory), "utf8"));
    expect(locale.runStatus.waiting_source).toBeTruthy();
    expect(locale.dryRun.actions.release).toBeTruthy();
    expect(locale.validation.list).toBeTruthy();
    for (const key of copyKeys) expect(locale.providerCopy[key]).toBeTruthy();
    for (const key of Object.keys(english.cloudflare)) expect(locale.cloudflare[key]).toBeTruthy();
    for (const key of Object.keys(english.cloudflare.phases)) expect(locale.cloudflare.phases[key]).toBeTruthy();
  }
  expect(providerCopy("Cloudflare Browser Run", (key) => key)).toBe("Cloudflare Browser Run");
  expect(providerCopy("URL discovery", (key) => key)).toBe("providerCopy.cloudflareDiscovery");
});
