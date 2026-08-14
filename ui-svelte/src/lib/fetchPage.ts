import type { ToolDef } from "./types";

// Contract for the `fetch_page` tool. The fetch itself is server-side
// (internal/server/fetchpage.go) — the browser never makes this request: it
// would be blocked by CORS, and the server needs the SSRF guard anyway since
// the URL comes from the model.
export const FETCH_PAGE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "fetch_page",
    description:
      "Read one web page and return its text plus any structured (schema.org) data it carries - use it when a search snippet is not enough: exact prices, specs, availability, dates, or the details of an article. Cannot run JavaScript, so some pages return an error instead of their content.",
    parameters: {
      type: "object",
      properties: {
        url: { type: "string", description: "Full http(s) URL of the page to read" },
      },
      required: ["url"],
    },
  },
};
