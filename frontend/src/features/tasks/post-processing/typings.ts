/** Ambient declarations of the post-processing runtime, for Monaco IntelliSense. */
export const RUNTIME_TYPINGS = `
/** Document content: text, or binary data for files. */
type ProcessInput = string | ArrayBuffer;

/** Return a string or binary to retain, an object to also change file metadata, or null/undefined to skip. */
type ProcessOutput =
  | string
  | ArrayBuffer
  | ArrayBufferView
  | null
  | undefined
  | {
      /** New content. null skips the document. */
      content: string | ArrayBuffer | ArrayBufferView | null;
      /** File name used when retaining binary content. */
      fileName?: string;
      /** MIME type used when retaining binary content. */
      mimeType?: string;
      /** Human-readable context passed to Hindsight. */
      context?: string;
    };

interface ProcessContext {
  task: {
    id: string;
    name: string;
    /** Source connector type, e.g. "notion". */
    sourceType: string;
    /** Destination Hindsight bank. */
    bankId: string;
  };
  run: {
    /** Run id; empty in dry runs and tests. */
    id?: string;
    /** incremental | full | reingest */
    mode?: string;
    dryRun: boolean;
    full: boolean;
    generation: number;
  };
  item: {
    id: string;
    name: string;
    path: string;
    /** page | document | file */
    kind: string;
    revision: string;
    /** ISO 8601 timestamp, or "" when unknown. */
    modifiedAt: string;
    mimeType: string;
    size: number;
    tags: string[];
    metadata: Record<string, string>;
    /** Typed attributes, as used by filters. */
    attributes: Record<string, unknown>;
  };
  document: {
    /** "binary" when input is an ArrayBuffer. */
    contentType: "text" | "binary";
    fileName: string;
    mimeType: string;
    /** Context passed to Hindsight. */
    context: string;
    size: number;
  };
}

interface KeyValue { key: string; value: string }

interface ExtractionOptions {
  /** Complete upload endpoint; for MinerU, the service base URL without /v1 or /api/v4. */
  url: string;
  apiKey?: string;
  headers?: KeyValue[];
  /** Extra multipart fields for Unstructured, Kreuzberg and Docling. */
  form?: KeyValue[];
  query?: KeyValue[];
}

interface StepLibrary {
  /** Uploads the current input and joins the returned element text. */
  unstructured(context: ProcessContext, input: ProcessInput | null, options: ExtractionOptions): Promise<string | null>;
  /** Uploads the current input and returns extracted content. */
  kreuzberg(context: ProcessContext, input: ProcessInput | null, options: ExtractionOptions): Promise<string | null>;
  /** Converts the current input to Markdown using docling-serve. */
  docling(context: ProcessContext, input: ProcessInput | null, options: ExtractionOptions): Promise<string | null>;
  /** Uploads, polls and downloads Markdown until completion or task cancellation. */
  mineru(context: ProcessContext, input: ProcessInput | null, options: Pick<ExtractionOptions, "url" | "apiKey" | "headers"> & { apiMode?: "self_hosted" | "cloud" }): Promise<string | null>;
  /** Replaces {{path}} placeholders (input, task.*, run.*, item.*, document.*). Set json to escape values for JSON strings. */
  template(context: ProcessContext, input: ProcessInput | null, text: string, json?: boolean): string;
  /** Replaces every match. With regex, the replacement supports $1 references. */
  replace<T>(context: ProcessContext, input: T, options: { pattern: string; replacement?: string; regex?: boolean; ignoreCase?: boolean }): T;
  trim<T>(context: ProcessContext, input: T, options: { side?: "both" | "start" | "end"; characters?: string }): T;
  prepend<T>(context: ProcessContext, input: T, options: { text?: string; separator?: string }): T;
  append<T>(context: ProcessContext, input: T, options: { text?: string; separator?: string }): T;
  truncate<T>(context: ProcessContext, input: T, options: { maxLength: number; suffix?: string }): T;
  case<T>(context: ProcessContext, input: T, options: { case?: "lower" | "upper" }): T;
  collapseWhitespace<T>(context: ProcessContext, input: T, options?: {}): T;
  stripHtml<T>(context: ProcessContext, input: T, options?: {}): T;
  /** Removes matching lines; with invert, keeps only matching lines. */
  removeLines<T>(context: ProcessContext, input: T, options: { pattern: string; regex?: boolean; ignoreCase?: boolean; invert?: boolean }): T;
  /** Returns null (skip) when the text matches; with invert, when it does not. */
  skip<T>(context: ProcessContext, input: T, options: { pattern: string; regex?: boolean; ignoreCase?: boolean; invert?: boolean }): T | null;
  /** Sends the document to an HTTP endpoint and uses the response. */
  http(context: ProcessContext, input: ProcessInput | null, options: {
    method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
    url: string;
    headers?: KeyValue[];
    query?: KeyValue[];
    bodyType?: "none" | "input" | "text" | "json" | "form";
    body?: string;
    form?: KeyValue[];
    responseMode?: "text" | "json" | "binary" | "ignore";
    /** Dot path into a JSON response, e.g. "choices.0.message.content". */
    responsePath?: string;
  }): Promise<ProcessInput | null>;
}

/** Built-in steps used by the UI pipeline. Text steps return binary input unchanged. */
declare const steps: StepLibrary;

interface FetchInit {
  method?: string;
  headers?: Headers | Record<string, string> | [string, string][];
  body?: string | ArrayBuffer | ArrayBufferView | URLSearchParams | null;
  /** Request timeout in milliseconds (default 60000). */
  timeout?: number;
}

declare class Headers implements Iterable<[string, string]> {
  constructor(init?: Headers | Record<string, string> | [string, string][]);
  append(name: string, value: string): void;
  set(name: string, value: string): void;
  get(name: string): string | null;
  has(name: string): boolean;
  delete(name: string): void;
  forEach(fn: (value: string, name: string, headers: Headers) => void): void;
  entries(): IterableIterator<[string, string]>;
  keys(): IterableIterator<string>;
  values(): IterableIterator<string>;
  [Symbol.iterator](): IterableIterator<[string, string]>;
}

declare class Response {
  readonly ok: boolean;
  readonly status: number;
  readonly statusText: string;
  readonly url: string;
  readonly headers: Headers;
  readonly bodyUsed: boolean;
  text(): Promise<string>;
  json(): Promise<any>;
  arrayBuffer(): Promise<ArrayBuffer>;
  bytes(): Promise<Uint8Array>;
}

/** Performs an HTTP request through the configured proxy. Responses are limited to 64 MB. */
declare function fetch(url: string, init?: FetchInit): Promise<Response>;

declare class URLSearchParams implements Iterable<[string, string]> {
  constructor(init?: string | Record<string, string> | [string, string][] | URLSearchParams);
  append(name: string, value: string): void;
  set(name: string, value: string): void;
  get(name: string): string | null;
  getAll(name: string): string[];
  has(name: string): boolean;
  delete(name: string): void;
  forEach(fn: (value: string, name: string, params: URLSearchParams) => void): void;
  entries(): IterableIterator<[string, string]>;
  keys(): IterableIterator<string>;
  values(): IterableIterator<string>;
  [Symbol.iterator](): IterableIterator<[string, string]>;
  toString(): string;
}

declare class TextEncoder {
  readonly encoding: "utf-8";
  encode(input?: string): Uint8Array;
}

declare class TextDecoder {
  readonly encoding: "utf-8";
  decode(input?: ArrayBuffer | ArrayBufferView): string;
}

declare function atob(data: string): string;
declare function btoa(data: string): string;

/** Console output is written to the run log. */
declare const console: {
  log(...data: any[]): void;
  info(...data: any[]): void;
  debug(...data: any[]): void;
  warn(...data: any[]): void;
  error(...data: any[]): void;
};
`;
