import { describe, it, expect } from "vitest";
import {
  buildRequest,
  errorMessage,
  levelProbs,
  newQuestion,
  nextQuestionId,
  normalizeDrafts,
  parseLoose,
  scorePercent,
  sortedProbs,
  validate,
  type DraftQuestion,
} from "./decisionApi";

function choiceQ(over: Partial<DraftQuestion> = {}): DraftQuestion {
  return {
    ...newQuestion("route"),
    instructions: "Where should this go?",
    choices: [
      { option: "billing", description: "money questions" },
      { option: "support", description: "" },
    ],
    ...over,
  };
}

describe("parseLoose", () => {
  it("sends a JSON object or array as JSON", () => {
    expect(parseLoose('{"a": 1}')).toEqual({ a: 1 });
    expect(parseLoose("[1, 2]")).toEqual([1, 2]);
  });
  it("keeps prose, bare numbers and broken JSON as the typed string", () => {
    expect(parseLoose("hello world")).toBe("hello world");
    expect(parseLoose("42")).toBe("42");
    expect(parseLoose('"quoted"')).toBe('"quoted"');
    expect(parseLoose("{not json")).toBe("{not json");
  });
});

describe("buildRequest", () => {
  it("shapes a choice question, nulling empty descriptions", () => {
    const req = buildRequest("kev", "customer is angry", [choiceQ()]);
    expect(req).toEqual({
      model: "kev",
      state: "customer is angry",
      questions: {
        route: {
          type: "choice",
          instructions: "Where should this go?",
          criteria: { billing: "money questions", support: null },
        },
      },
    });
  });

  it("drops blank choice rows and trims names", () => {
    const q = choiceQ({ choices: [{ option: " a ", description: "x" }, { option: "", description: "" }, { option: "b", description: "" }] });
    expect(buildRequest("m", "s", [q]).questions.route.criteria).toEqual({ a: "x", b: null });
  });

  it("sends score levels in order", () => {
    const q = { ...newQuestion("sev", "score"), instructions: "How bad?", levels: ["fine", " bad ", "awful"] };
    expect(buildRequest("m", "s", [q]).questions.sev.criteria).toEqual(["fine", "bad", "awful"]);
  });

  it("omits noul criteria when empty and sends only the filled halves otherwise", () => {
    const q = { ...newQuestion("ok", "noul"), instructions: "Is it fine?" };
    expect("criteria" in buildRequest("m", "s", [q]).questions.ok).toBe(false);
    expect(buildRequest("m", "s", [{ ...q, trueDesc: "yes it is" }]).questions.ok.criteria).toEqual({ true: "yes it is" });
  });

  it("uses a null state when only files are given, and attaches files", () => {
    const req = buildRequest("m", "  ", [choiceQ()], ["data:image/png;base64,AA"]);
    expect(req.state).toBeNull();
    expect(req.files).toEqual(["data:image/png;base64,AA"]);
    expect("files" in buildRequest("m", "s", [choiceQ()])).toBe(false);
  });

  it("sends structured instructions as JSON", () => {
    const req = buildRequest("m", '{"k": "v"}', [choiceQ({ instructions: '["a", "b"]' })]);
    expect(req.state).toEqual({ k: "v" });
    expect(req.questions.route.instructions).toEqual(["a", "b"]);
  });
});

describe("validate", () => {
  it("passes a complete request", () => {
    expect(validate("m", [choiceQ()])).toEqual([]);
  });
  it("catches the empty required fields", () => {
    expect(validate("", [])).toHaveLength(2);
  });
  it("accepts a file in place of a state", () => {
    expect(validate("m", [choiceQ()])).toEqual([]);
  });
  it("flags missing and duplicate ids", () => {
    const errs = validate("m", [choiceQ({ id: "" }), choiceQ({ id: "a" }), choiceQ({ id: "a" })]);
    expect(errs.some((e) => e.includes("needs an id"))).toBe(true);
    expect(errs.some((e) => e.includes("used twice"))).toBe(true);
  });
  it("leaves a null state and blank instructions to the server", () => {
    const q = { ...newQuestion("n", "noul"), instructions: " " };
    expect(validate("m", [q])).toEqual([]);
    const req = buildRequest("m", "", [q]);
    expect(req.state).toBeNull();
    expect("instructions" in req.questions.n).toBe(false);
  });
  it("needs two named choices", () => {
    expect(validate("m", [choiceQ({ choices: [{ option: "only", description: "" }] })]).join()).toContain("at least 2 options");
    expect(validate("m", [choiceQ({ choices: [{ option: "", description: "orphan" }, { option: "b", description: "" }] })]).join()).toContain("no name");
    expect(validate("m", [choiceQ({ choices: [{ option: "a", description: "" }, { option: "a", description: "" }] })]).join()).toContain("share a name");
  });
  it("needs every score level described, within 2 to 10", () => {
    const q = { ...newQuestion("s", "score"), instructions: "x", levels: ["a", ""] };
    expect(validate("m", [q]).join()).toContain("every score level");
    expect(validate("m", [{ ...q, levels: ["a"] }]).join()).toContain("2 to 10 levels");
    expect(validate("m", [{ ...q, levels: ["a", "b"] }])).toEqual([]);
  });
  it("a noul question needs nothing but instructions", () => {
    expect(validate("m", [{ ...newQuestion("n", "noul"), instructions: "true?" }])).toEqual([]);
  });
});

describe("drafts", () => {
  it("numbers new ids past the ones in use", () => {
    expect(nextQuestionId([])).toBe("q1");
    expect(nextQuestionId([{ id: "q1" }])).toBe("q2");
    expect(nextQuestionId([{ id: "q2" }])).toBe("q3");
    expect(nextQuestionId([{ id: "q2" }, { id: "x" }])).toBe("q3");
  });
  it("repairs a damaged saved set instead of crashing the builder", () => {
    expect(normalizeDrafts(null)).toEqual([]);
    const [q] = normalizeDrafts([{ id: "a", type: "bogus", choices: "nope", levels: [1, "x"] }, null, 3]);
    expect(q.type).toBe("choice");
    expect(q.choices).toHaveLength(2);
    expect(q.levels).toEqual(["", "x"]);
  });
});

describe("answers", () => {
  it("sorts probabilities descending", () => {
    expect(sortedProbs({ a: 0.1, b: 0.7, c: 0.2 }).map(([k]) => k)).toEqual(["b", "c", "a"]);
    expect(sortedProbs(undefined)).toEqual([]);
  });
  it("orders score levels numerically with their legend", () => {
    const rows = levelProbs({ type: "score", score: 1.2, legend: { "0": "low", "1": "mid", "10": "top" }, probabilities: { "10": 0.1, "0": 0.3, "1": 0.6 } });
    expect(rows.map((r) => r.level)).toEqual([0, 1, 10]);
    expect(rows[1].label).toBe("mid");
  });
  it("places the expected score on the scale", () => {
    expect(scorePercent(0, 5)).toBe(0);
    expect(scorePercent(2, 5)).toBe(50);
    expect(scorePercent(9, 5)).toBe(100);
    expect(scorePercent(1, 1)).toBe(0);
  });
});

describe("errorMessage", () => {
  it("reads llama-server's envelope", () => {
    expect(errorMessage(400, '{"error":{"message":"questions.q1: bad"}}')).toBe("400: questions.q1: bad");
  });
  it("falls back to plain text and explains 501", () => {
    expect(errorMessage(502, "bad gateway\n")).toBe("502: bad gateway");
    expect(errorMessage(501, '{"error":{"message":"not a decision model"}}')).toContain("not a decision model");
    expect(errorMessage(501, "")).toContain("no image support");
  });
});
