# Document extraction in post-processing

Add **Unstructured**, **Kreuzberg**, **Docling**, or **MinerU** in a task's post-processing steps. Each step uploads the current document to a remote Web API and passes the extracted text to subsequent steps and Hindsight. The same steps are available in code mode with editor completion. No parser, model, or container is installed or started by this integration.

| Step | URL to configure | API key header | Output |
| --- | --- | --- | --- |
| Unstructured | Complete Partition endpoint, e.g. `https://service.example/general/v0/general` | `unstructured-api-key` | Element text joined with blank lines |
| Kreuzberg | Complete extraction endpoint, e.g. `https://service.example/extract` | `Authorization: Bearer …` (if required by the deployment) | Extracted content |
| Docling | Complete docling-serve endpoint, e.g. `https://service.example/v1/convert/file` | `X-Api-Key` | Markdown |
| MinerU, self-hosted (V1) | Service base URL, e.g. `https://service.example` | `Authorization: Bearer …` | Markdown from the result ZIP |
| MinerU Cloud | `https://mineru.net` | `Authorization: Bearer …` (required) | Markdown from the result ZIP |

Unstructured, Kreuzberg and Docling send multipart uploads in the `files` field. Optional form fields allow service-specific settings, for example `strategy=hi_res` for Unstructured, `config={"force_ocr":true}` for Kreuzberg, or `do_ocr=true` for Docling. The `files` field is reserved. Docling also reserves `to_formats`, `target_type` and `image_export_mode` so the response contains Markdown directly, without embedding images.

MinerU self-hosted uses the current **V1 Tasks API**: create a single-file manifest at `/v1/tasks`, upload to the returned URL, poll `/v1/tasks/{task_id}`, and download the completed file's result. It requests all pages and the default output archive. Older MinerU deployments exposing only `/file_parse` are not compatible with this mode.

MinerU Cloud uses the official **V4 precise parsing API**: `/api/v4/file-urls/batch`, PUT to the signed upload URL, then `/api/v4/extract-results/batch/{batch_id}`. Model and OCR settings use the service defaults. API keys and custom API headers are not forwarded to a different storage origin; redirects use the existing credential isolation rules.

The current input is uploaded, including changes from preceding steps. Binary input preserves its filename and MIME type; text input is sent as UTF-8 `document.txt`. A skipped document does not trigger an API call. HTTP errors, malformed results, incomplete conversions and failed MinerU tasks fail the step rather than retaining partial text. Unstructured can legitimately return an empty element array; an explicitly empty successful text result remains empty.

Existing runtime limits apply: 16 MiB input/output, 64 MiB per HTTP response and 60 seconds per request. There is no fixed timeout for the entire document pipeline or MinerU polling. The caller's deadline or task cancellation interrupts script initialization, execution, requests and polling; it does not cancel an already-submitted remote job. Without a caller deadline, a script loop runs until the task is canceled. Retrying the document may create another remote job. ZIPs are inspected in memory, with bounded Markdown decompression, and are never unpacked onto disk. Images from the archive are not uploaded separately.

```js
/** @param {ProcessContext} context @param {ProcessInput} input */
async function process(context, input) {
  return await steps.docling(context, input, {
    url: "https://docling.example/v1/convert/file",
    form: [{ key: "do_ocr", value: "true" }],
  });
}
```

Protocol references: [Unstructured Partition API](https://github.com/Unstructured-IO/unstructured-api), [Kreuzberg API server](https://docs.kreuzberg.dev/guides/api-server/), [docling-serve](https://github.com/docling-project/docling-serve/blob/main/docs/usage.md), [MinerU V1 contracts](https://github.com/opendatalab/MinerU/blob/master/mineru/tasks/contracts.py), [MinerU Cloud API](https://mineru.net/apiManage/docs).

Tests use lightweight HTTP fixtures for upload encoding, authentication, polling, result validation and cancellation. Real remote parser acceptance requires an available service and its credentials.
