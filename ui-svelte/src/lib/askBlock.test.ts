import { describe, it, expect } from "vitest";
import { parseAskBlock, composeAskAnswer, splitAsk, isOtherOption } from "./askBlock";

describe("parseAskBlock", () => {
  const block = (json: string) => "What are you after?\n\n```ask\n" + json + "\n```\n";

  it("parses questions and strips the fence from the prose", () => {
    const r = parseAskBlock(
      block('{"questions":[{"id":"budget","label":"Budget","type":"single","options":["under 300","300-600"]}]}'),
    );
    expect(r).not.toBeNull();
    expect(r!.questions).toHaveLength(1);
    expect(r!.questions[0]).toMatchObject({ id: "budget", label: "Budget", type: "single", options: ["under 300", "300-600"] });
    expect(r!.cleaned).toBe("What are you after?");
    expect(r!.cleaned).not.toContain("```");
  });

  it("accepts a bare array and normalizes multi/text", () => {
    const r = parseAskBlock(block('[{"label":"Brands","type":"multiple","options":["Samsung","Google"]},{"label":"Anything else"}]'));
    expect(r!.questions[0].type).toBe("multi");
    // No options → a free-text question, which always allows typing.
    expect(r!.questions[1]).toMatchObject({ type: "text", options: [], allowOther: true });
    expect(r!.questions[1].id).toBe("q2");
  });

  // A broken block must fall through to being rendered as an ordinary code
  // fence — never swallow the message.
  it("returns null for malformed or empty blocks", () => {
    expect(parseAskBlock(block("not json"))).toBeNull();
    expect(parseAskBlock(block('{"questions":[]}'))).toBeNull();
    expect(parseAskBlock(block('{"questions":[{"options":["a"]}]}'))).toBeNull(); // no label
    expect(parseAskBlock("no block here")).toBeNull();
  });
});

describe("splitAsk", () => {
  // Mid-stream the fence is still open — the user must never see growing JSON.
  it("hides a half-written block and reports it pending", () => {
    const r = splitAsk('Two quick things.\n\n```ask\n{"questions":[{"id":"b","label":"Bud');
    expect(r.pending).toBe(true);
    expect(r.questions).toBeNull();
    expect(r.cleaned).toBe("Two quick things.");
  });

  it("hides the bare opening fence as soon as it appears", () => {
    expect(splitAsk("Two quick things.\n\n```ask").cleaned).toBe("Two quick things.");
    expect(splitAsk("Two quick things.\n\n```ask").pending).toBe(true);
  });

  it("returns parsed questions once the fence closes", () => {
    const r = splitAsk('Pick:\n```ask\n{"questions":[{"id":"b","label":"Budget","options":["x"]}]}\n```');
    expect(r.pending).toBe(false);
    expect(r.questions).toHaveLength(1);
    expect(r.cleaned).toBe("Pick:");
  });

  // Closed but broken: leave it in the text as an ordinary code block instead of
  // hiding content behind a spinner that never resolves.
  it("leaves a closed-but-invalid block in the prose", () => {
    const r = splitAsk("Pick:\n```ask\nnot json\n```");
    expect(r).toMatchObject({ pending: false, questions: null });
    expect(r.cleaned).toContain("not json");
  });
});

describe("composeAskAnswer", () => {
  it("joins picks per question and marks skipped ones", () => {
    const qs = parseAskBlock(
      "?\n```ask\n" +
        '{"questions":[{"id":"b","label":"Budget","options":["x"]},{"id":"k","label":"Brands","type":"multi","options":["a","b"]}]}' +
        "\n```",
    )!.questions;
    const out = composeAskAnswer(qs, { k: ["a", "b"] });
    expect(out).toBe("Budget: no preference\nBrands: a, b");
  });
});

describe("isOtherOption", () => {
  it("recognises the escape hatches models write", () => {
    for (const s of ["Other", "other…", "Other (please specify)", "Something else", "None of the above", "Custom", "Write my own"])
      expect(isOtherOption(s), s).toBe(true);
  });

  // These are real answers. Treating one as "type something" would strand the
  // user on a step with an empty field and no way to say what they picked.
  it("leaves real answers alone", () => {
    for (const s of ["Samsung", "No preference", "Not sure", "Other brands I already own", "USD"])
      expect(isOtherOption(s), s).toBe(false);
  });
});
