import type { ToolDef } from "./types";

// Contracts for the small "personal assistant" tools. All five are dispatched
// server-side (internal/server/{datetime,calc,units,weather,feed}.go) — the
// browser never calls them.
//
// Split into two groups by cost, because every advertised tool is bytes in the
// KV-stable prefix of every turn:
//   ALWAYS_TOOLS — local, no network, and universally applicable. Cheap enough
//     to carry on every chat.
//   EXTRA_TOOLS  — network-backed and situational. Behind the "Weather & feeds"
//     toggle in the Configs popover.

// A model has no clock: without a search in the turn, today's date never
// reaches it and it answers from its training cutoff.
export const GET_DATETIME_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "get_datetime",
    description:
      "Get the current date, time and weekday, optionally in a named timezone, and optionally the number of days between today and another date. Use it for anything time-sensitive - whether a date has passed, what day something falls on, how long until a deadline - rather than assuming today's date.",
    parameters: {
      type: "object",
      properties: {
        timezone: { type: "string", description: "IANA timezone name (Europe/Bucharest, America/New_York, UTC). Defaults to the server's own zone." },
        until: { type: "string", description: "Optional date (YYYY-MM-DD) to count the days to or from" },
      },
    },
  },
};

// The arithmetic guard. Deliberately described as arithmetic-only so the model
// does not try to send it code.
export const CALCULATE_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "calculate",
    description:
      "Evaluate an arithmetic expression exactly. Use it for any calculation you would otherwise do in your head - totals, price per unit, percentages, running costs, differences between options. Supports + - * / ^, parentheses, % for percent (20% is 0.2), and sqrt/abs/round/floor/ceil/min/max/sum/avg/pow/ln/log. Arithmetic only: no variables, no units, no code.",
    parameters: {
      type: "object",
      properties: {
        expression: { type: "string", description: "The expression, e.g. \"(1299 * 3) / 36\" or \"250 * (1 - 15%)\"" },
      },
      required: ["expression"],
    },
  },
};

export const CONVERT_UNITS_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "convert_units",
    description:
      "Convert a measurement between units - length, mass, volume, area, speed, power, energy, data, time, pressure and temperature. Use it whenever a source states a figure in units the user does not think in (a spec sheet in inches and pounds, a drive in TB against an OS reporting TiB), instead of converting from memory.",
    parameters: {
      type: "object",
      properties: {
        amount: { type: "number", description: "Amount to convert (defaults to 1)" },
        from: { type: "string", description: "Unit of the amount, e.g. in, lb, floz, mph, GB, C" },
        to: { type: "string", description: "Unit to convert into, e.g. cm, kg, ml, km/h, GiB, F" },
      },
      required: ["from", "to"],
    },
  },
};

export const GET_WEATHER_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "get_weather",
    description:
      "Get the current weather and a short forecast for a place, by name. Use it for anything that depends on real conditions - what to wear, whether an outdoor plan holds, travel timing - and never describe the weather from memory.",
    parameters: {
      type: "object",
      properties: {
        location: { type: "string", description: "Place name, e.g. \"Cluj-Napoca\" or \"Paris, France\"" },
        days: { type: "number", description: "Forecast days including today, 1-7 (default 3)" },
        units: { type: "string", description: "\"metric\" (default) or \"imperial\"" },
      },
      required: ["location"],
    },
  },
};

export const FETCH_FEED_TOOL: ToolDef = {
  type: "function",
  function: {
    name: "fetch_feed",
    description:
      "Read one RSS or Atom feed and return its newest entries in order. Use it for \"what's new on X\" when the site publishes a feed - a search engine ranks by relevance and will hand you last year's article, while a feed is ordered by time. Returns headlines and blurbs only: call fetch_page on an entry's link before summarising what it says.",
    parameters: {
      type: "object",
      properties: {
        url: { type: "string", description: "Full http(s) URL of the RSS/Atom feed (often /feed, /rss.xml or /atom.xml)" },
        limit: { type: "number", description: "How many entries to return, 1-15 (default 15)" },
      },
      required: ["url"],
    },
  },
};

// Local, always-on: no network, no key, no rate limit, and the failures they
// prevent (wrong date, wrong arithmetic, wrong conversion) happen in ordinary
// conversation, not just in one mode.
export const ALWAYS_TOOLS: ToolDef[] = [GET_DATETIME_TOOL, CALCULATE_TOOL, CONVERT_UNITS_TOOL];

// Network-backed and situational — gated on the "Weather & feeds" toggle.
export const EXTRA_TOOLS: ToolDef[] = [GET_WEATHER_TOOL, FETCH_FEED_TOOL];
