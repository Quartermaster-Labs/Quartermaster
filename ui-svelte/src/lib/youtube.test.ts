import { describe, it, expect } from "vitest";
import { extractYouTubeIds, MAX_UNFURLS } from "./youtube";

describe("extractYouTubeIds", () => {
  it("finds a watch link mid-sentence", () => {
    expect(extractYouTubeIds("look at https://www.youtube.com/watch?v=dQw4w9WgXcQ ok")).toEqual(["dQw4w9WgXcQ"]);
  });

  it("handles youtu.be, shorts, embed, live and nocookie", () => {
    expect(extractYouTubeIds("https://youtu.be/dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
    expect(extractYouTubeIds("https://www.youtube.com/shorts/dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
    expect(extractYouTubeIds("https://www.youtube.com/embed/dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
    expect(extractYouTubeIds("https://www.youtube.com/live/dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
    expect(extractYouTubeIds("https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
  });

  it("finds v= when it is not the first query param", () => {
    expect(extractYouTubeIds("https://www.youtube.com/watch?list=PL123&v=dQw4w9WgXcQ")).toEqual(["dQw4w9WgXcQ"]);
  });

  it("keeps a link with a timestamp suffix", () => {
    expect(extractYouTubeIds("https://youtu.be/dQw4w9WgXcQ?t=42")).toEqual(["dQw4w9WgXcQ"]);
  });

  it("dedupes repeats but keeps order", () => {
    const text = "https://youtu.be/aaaaaaaaaaa and https://youtu.be/bbbbbbbbbbb and https://youtu.be/aaaaaaaaaaa";
    expect(extractYouTubeIds(text)).toEqual(["aaaaaaaaaaa", "bbbbbbbbbbb"]);
  });

  it("caps at MAX_UNFURLS", () => {
    const text = ["aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "ddddddddddd"]
      .map((id) => `https://youtu.be/${id}`)
      .join(" ");
    expect(extractYouTubeIds(text)).toHaveLength(MAX_UNFURLS);
  });

  it("ignores non-video youtube URLs and lookalikes", () => {
    expect(extractYouTubeIds("https://www.youtube.com/@someChannel")).toEqual([]);
    expect(extractYouTubeIds("https://www.youtube.com/results?search_query=cats")).toEqual([]);
    expect(extractYouTubeIds("https://vimeo.com/watch?v=dQw4w9WgXcQ")).toEqual([]);
    // A bare id in prose is NOT a link — the tool accepts one, a message doesn't.
    expect(extractYouTubeIds("the video id is dQw4w9WgXcQ")).toEqual([]);
  });

  it("does not truncate a longer id-like token to 11 chars", () => {
    expect(extractYouTubeIds("https://youtu.be/dQw4w9WgXcQextra")).toEqual([]);
  });

  it("returns empty for empty input", () => {
    expect(extractYouTubeIds("")).toEqual([]);
  });
});
