// Formatting for request-log rows.
//
// These lived inside Activity.svelte until the dashboard grew a "Recent" band
// that renders the same records in miniature. Two views of one log must not
// drift on how they print a duration, so the pure formatters moved here — the
// split rule in ui-svelte/CLAUDE.md: non-reactive helpers leave the component,
// anything touching $state stays.

export function formatSpeed(speed: number): string {
  return speed < 0 ? "-" : speed.toFixed(1);
}

export function formatDuration(ms: number): string {
  return (ms / 1000).toFixed(2) + "s";
}

// `nowMs` is a parameter rather than a Date.now() call so the function is
// testable; every call site outside the tests takes the default.
export function formatRelativeTime(timestamp: string, nowMs: number = Date.now()): string {
  const diffInSeconds = Math.floor((nowMs - new Date(timestamp).getTime()) / 1000);

  // Handle future dates by returning "just now"
  if (diffInSeconds < 5) {
    return "now";
  }
  if (diffInSeconds < 60) {
    return `${diffInSeconds}s ago`;
  }
  const diffInMinutes = Math.floor(diffInSeconds / 60);
  if (diffInMinutes < 60) {
    return `${diffInMinutes}m ago`;
  }
  const diffInHours = Math.floor(diffInMinutes / 60);
  if (diffInHours < 24) {
    return `${diffInHours}h ago`;
  }
  return "a while ago";
}

// "/v1/chat/completions" -> "chat/completions". The version prefix is the same
// on every row, so it is column width spent on nothing.
export function shortReqPath(path: string): string {
  return path.replace(/^\/?v1\//, "").replace(/^\//, "");
}
