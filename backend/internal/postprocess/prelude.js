// Runtime library for post-processing scripts. Host functions prefixed with
// __ are provided by Go; everything else mirrors a small subset of Web APIs.
"use strict";

(function (global) {
  const host = {
    fetch: global.__fetch,
    log: global.__log,
    encode: global.__utf8Encode,
    decode: global.__utf8Decode,
    atob: global.__atob,
    btoa: global.__btoa,
    extract: global.__extract,
  };
  for (const name of ["__fetch", "__log", "__utf8Encode", "__utf8Decode", "__atob", "__btoa", "__extract"]) delete global[name];

  function isBinary(value) {
    return value instanceof ArrayBuffer || ArrayBuffer.isView(value);
  }

  function toBytes(value) {
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    return new Uint8Array(host.encode(String(value)));
  }

  function format(value) {
    if (typeof value === "string") return value;
    if (value instanceof Error) return value.stack || String(value);
    if (isBinary(value)) return "[binary " + toBytes(value).byteLength + " bytes]";
    try {
      const json = JSON.stringify(value);
      return json === undefined ? String(value) : json;
    } catch (_) {
      return String(value);
    }
  }

  const log = (level) => (...args) => host.log(level, args.map(format).join(" "));
  global.console = { log: log("info"), info: log("info"), debug: log("debug"), warn: log("warn"), error: log("error") };

  class TextEncoder {
    get encoding() { return "utf-8"; }
    encode(text = "") { return new Uint8Array(host.encode(String(text))); }
  }

  class TextDecoder {
    get encoding() { return "utf-8"; }
    decode(data) {
      if (data === undefined) return "";
      const bytes = toBytes(data);
      return host.decode(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength));
    }
  }

  function encodeComponent(value) {
    return encodeURIComponent(value).replace(/%20/g, "+");
  }

  function decodeComponent(value) {
    return decodeURIComponent(value.replace(/\+/g, " "));
  }

  class URLSearchParams {
    constructor(init) {
      this._entries = [];
      if (init instanceof URLSearchParams) this._entries = init._entries.map((e) => [...e]);
      else if (typeof init === "string") {
        for (const part of init.replace(/^\?/, "").split("&")) {
          if (!part) continue;
          const i = part.indexOf("=");
          this._entries.push(i < 0 ? [decodeComponent(part), ""] : [decodeComponent(part.slice(0, i)), decodeComponent(part.slice(i + 1))]);
        }
      } else if (Array.isArray(init)) this._entries = init.map(([k, v]) => [String(k), String(v)]);
      else if (init) this._entries = Object.keys(init).map((k) => [k, String(init[k])]);
    }
    append(key, value) { this._entries.push([String(key), String(value)]); }
    set(key, value) { this.delete(key); this.append(key, value); }
    get(key) { const e = this._entries.find(([k]) => k === key); return e ? e[1] : null; }
    getAll(key) { return this._entries.filter(([k]) => k === key).map(([, v]) => v); }
    has(key) { return this._entries.some(([k]) => k === key); }
    delete(key) { this._entries = this._entries.filter(([k]) => k !== key); }
    forEach(fn) { for (const [k, v] of this._entries) fn(v, k, this); }
    entries() { return this._entries.map((e) => [...e])[Symbol.iterator](); }
    keys() { return this._entries.map(([k]) => k)[Symbol.iterator](); }
    values() { return this._entries.map(([, v]) => v)[Symbol.iterator](); }
    [Symbol.iterator]() { return this.entries(); }
    toString() { return this._entries.map(([k, v]) => encodeComponent(k) + "=" + encodeComponent(v)).join("&"); }
  }

  class Headers {
    constructor(init) {
      this._map = new Map();
      if (init instanceof Headers) init.forEach((v, k) => this.append(k, v));
      else if (Array.isArray(init)) for (const [k, v] of init) this.append(k, v);
      else if (init) for (const k of Object.keys(init)) this.append(k, init[k]);
    }
    append(key, value) {
      const k = String(key).toLowerCase();
      const prev = this._map.get(k);
      this._map.set(k, prev === undefined ? String(value) : prev + ", " + value);
    }
    set(key, value) { this._map.set(String(key).toLowerCase(), String(value)); }
    get(key) { const v = this._map.get(String(key).toLowerCase()); return v === undefined ? null : v; }
    has(key) { return this._map.has(String(key).toLowerCase()); }
    delete(key) { this._map.delete(String(key).toLowerCase()); }
    forEach(fn) { for (const [k, v] of this._map) fn(v, k, this); }
    entries() { return this._map.entries(); }
    keys() { return this._map.keys(); }
    values() { return this._map.values(); }
    [Symbol.iterator]() { return this._map.entries(); }
  }

  class Response {
    constructor(raw) {
      this.status = raw.status;
      this.statusText = raw.statusText;
      this.url = raw.url;
      this.ok = raw.status >= 200 && raw.status < 300;
      this.headers = new Headers(raw.headers);
      this._body = raw.body;
      this.bodyUsed = false;
    }
    _consume() {
      if (this.bodyUsed) throw new TypeError("Body has already been consumed");
      this.bodyUsed = true;
      return this._body;
    }
    async arrayBuffer() { return this._consume(); }
    async bytes() { return new Uint8Array(this._consume()); }
    async text() { return host.decode(this._consume()); }
    async json() { return JSON.parse(await this.text()); }
  }

  async function fetch(resource, init = {}) {
    const url = typeof resource === "string" ? resource : String(resource && resource.url !== undefined ? resource.url : resource);
    const headers = new Headers(init.headers);
    let body;
    if (init.body !== undefined && init.body !== null) {
      if (init.body instanceof URLSearchParams) {
        if (!headers.has("content-type")) headers.set("content-type", "application/x-www-form-urlencoded;charset=UTF-8");
        body = init.body.toString();
      } else if (isBinary(init.body)) {
        const bytes = toBytes(init.body);
        body = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
      } else {
        if (!headers.has("content-type")) headers.set("content-type", "text/plain;charset=UTF-8");
        body = String(init.body);
      }
    }
    const raw = host.fetch({
      url,
      method: String(init.method || "GET").toUpperCase(),
      headers: [...headers],
      body,
      timeout: init.timeout,
    });
    return new Response(raw);
  }

  Object.assign(global, {
    TextEncoder, TextDecoder, URLSearchParams, Headers, Response, fetch,
    atob: (s) => host.atob(String(s)),
    btoa: (s) => host.btoa(String(s)),
  });

  // ---- Step library ---------------------------------------------------

  function escapeRegExp(s) {
    return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function compilePattern(o, extraFlags = "") {
    const source = o.regex ? o.pattern : escapeRegExp(o.pattern);
    return new RegExp(source, extraFlags + (o.ignoreCase ? "i" : ""));
  }

  function lookup(root, path) {
    let value = root;
    for (const part of String(path).split(".").filter(Boolean)) {
      if (value === null || value === undefined) return undefined;
      value = value[part];
    }
    return value;
  }

  function toBase64(value) {
    const bytes = toBytes(value);
    let s = "";
    for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    return host.btoa(s);
  }

  // Replaces {{path}} placeholders using input, task, run, item and document.
  function template(text, context, input, json) {
    const scope = Object.assign({}, context, { input });
    return String(text || "").replace(/\{\{\s*([\w.$-]+)\s*\}\}/g, (_, path) => {
      let value = path === "input" && isBinary(input) ? toBase64(input) : lookup(scope, path);
      if (value === undefined || value === null) value = "";
      else if (typeof value !== "string") value = JSON.stringify(value);
      return json ? JSON.stringify(value).slice(1, -1) : value;
    });
  }

  // Text steps pass binary documents and skipped (null) documents through unchanged.
  const textStep = (fn) => (context, input, options) =>
    typeof input === "string" ? fn(input, options || {}, context) : input;

  const extractionStep = (type) => async (context, input, options) => {
    if (input === null || input === undefined) return null;
    const o = Object.assign({}, options, { type });
    o.url = template(o.url, context, input);
    for (const field of ["headers", "form", "query"]) {
      o[field] = (o[field] || []).map(({ key, value }) => ({ key, value: template(value, context, input) }));
    }
    const binary = isBinary(input);
    return host.extract(o, toBytes(input), {
      fileName: binary ? context.document.fileName : "document.txt",
      mimeType: binary ? context.document.mimeType : "text/plain;charset=UTF-8",
    });
  };

  const steps = {
    unstructured: extractionStep("unstructured"),
    kreuzberg: extractionStep("kreuzberg"),
    docling: extractionStep("docling"),
    mineru: extractionStep("mineru"),
    template: (context, input, text, json = false) => template(text, context, input, json),

    replace: textStep((input, o) => {
      const re = compilePattern(o, "g");
      return o.regex ? input.replace(re, o.replacement || "") : input.replace(re, () => o.replacement || "");
    }),

    trim: textStep((input, o) => {
      const side = o.side || "both";
      if (!o.characters) return side === "start" ? input.trimStart() : side === "end" ? input.trimEnd() : input.trim();
      const set = "[" + escapeRegExp(o.characters).replace(/-/g, "\\-") + "]+";
      if (side !== "end") input = input.replace(new RegExp("^" + set), "");
      if (side !== "start") input = input.replace(new RegExp(set + "$"), "");
      return input;
    }),

    prepend: textStep((input, o, context) => template(o.text, context, input) + (o.separator || "") + input),

    append: textStep((input, o, context) => input + (o.separator || "") + template(o.text, context, input)),

    truncate: textStep((input, o) => {
      let count = 0, end = 0;
      for (const ch of input) {
        if (count++ === o.maxLength) return input.slice(0, end) + (o.suffix || "");
        end += ch.length;
      }
      return input;
    }),

    case: textStep((input, o) => (o.case === "upper" ? input.toUpperCase() : input.toLowerCase())),

    collapseWhitespace: textStep((input) =>
      input
        .replace(/\r\n?/g, "\n")
        .replace(/[ \t\f\v ]+/g, " ")
        .replace(/ +\n/g, "\n")
        .replace(/\n{3,}/g, "\n\n")
        .trim()),

    stripHtml: textStep((input) =>
      input
        .replace(/<(script|style)[^>]*>[\s\S]*?<\/\1>/gi, "")
        .replace(/<br\s*\/?>/gi, "\n")
        .replace(/<\/(p|div|li|tr|h[1-6])>/gi, "\n")
        .replace(/<[^>]+>/g, "")
        .replace(/&nbsp;/g, " ")
        .replace(/&lt;/g, "<")
        .replace(/&gt;/g, ">")
        .replace(/&quot;/g, "\"")
        .replace(/&#39;/g, "'")
        .replace(/&amp;/g, "&")),

    removeLines: textStep((input, o) => {
      const re = compilePattern(o);
      return input.split("\n").filter((line) => re.test(line) === !!o.invert).join("\n");
    }),

    skip: (context, input, o) => {
      if (input === null || input === undefined) return null;
      const text = typeof input === "string" ? input : "";
      const matched = compilePattern(o).test(text);
      return matched !== !!o.invert ? null : input;
    },

    http: async (context, input, o) => {
      if (input === null || input === undefined) return null;
      let url = template(o.url, context, input);
      const query = new URLSearchParams();
      for (const p of o.query || []) if (p.key) query.append(template(p.key, context, input), template(p.value, context, input));
      const qs = query.toString();
      if (qs) url += (url.includes("?") ? "&" : "?") + qs;

      const headers = new Headers();
      let body;
      switch (o.bodyType) {
        case "input":
          body = input;
          headers.set("content-type", isBinary(input) ? (context.document.mimeType || "application/octet-stream") : "text/plain;charset=UTF-8");
          break;
        case "text":
          body = template(o.body, context, input);
          headers.set("content-type", "text/plain;charset=UTF-8");
          break;
        case "json":
          body = template(o.body, context, input, true);
          headers.set("content-type", "application/json");
          break;
        case "form":
          body = new URLSearchParams();
          for (const f of o.form || []) if (f.key) body.append(template(f.key, context, input), template(f.value, context, input));
          break;
      }
      for (const h of o.headers || []) if (h.key) headers.set(h.key, template(h.value, context, input));

      const method = o.method || "POST";
      const res = await fetch(url, { method, headers, body: method === "GET" ? undefined : body });
      if (!res.ok) {
        const detail = (await res.text()).slice(0, 300);
        throw new Error(`HTTP step: ${method} ${url} returned ${res.status} ${res.statusText}${detail ? ": " + detail : ""}`);
      }
      switch (o.responseMode) {
        case "ignore":
          return input;
        case "binary":
          return res.arrayBuffer();
        case "json": {
          const data = await res.json();
          const value = o.responsePath ? lookup(data, o.responsePath) : data;
          if (value === undefined || value === null) throw new Error(`HTTP step: response has no value at "${o.responsePath}"`);
          return typeof value === "string" ? value : JSON.stringify(value, null, 2);
        }
        default:
          return res.text();
      }
    },
  };
  global.steps = Object.freeze(steps);

  // Normalizes the user function's result for the host.
  global.__run = async function (context, input) {
    if (typeof process !== "function") throw new Error("Define a function named process(context, input)");
    const out = await process(context, input);
    if (out === null || out === undefined) return { kind: "skip" };
    const result = (typeof out === "object" && !isBinary(out)) ? out : { content: out };
    const meta = {
      fileName: result.fileName === undefined ? undefined : String(result.fileName),
      mimeType: result.mimeType === undefined ? undefined : String(result.mimeType),
      context: result.context === undefined ? undefined : String(result.context),
    };
    const content = result.content;
    if (content === null || content === undefined) return { kind: "skip" };
    if (isBinary(content)) return Object.assign(meta, { kind: "binary", data: toBytes(content) });
    return Object.assign(meta, { kind: "text", text: String(content) });
  };
})(globalThis);
