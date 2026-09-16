import { describe, it, expect } from "vitest";
import {
  BACKEND_CLASSES,
  backendClass,
  backendClasses,
  backendServesClass,
  backendOptionLabel,
  backendOptionDetail,
  backendPickOptions,
  hideDuplicateBuildRows,
  type BackendPickEntry,
} from "./backends";

function row(over: Partial<BackendPickEntry>): BackendPickEntry {
  return { id: "x", kind: "sd", name: "stable-diffusion.cpp", path: "", ...over };
}

describe("backendOptionLabel", () => {
  it("names the variant, so two builds of one engine are tellable apart", () => {
    expect(backendOptionLabel(row({ variant: "vulkan" }))).toBe("stable-diffusion.cpp (vulkan)");
    expect(backendOptionLabel(row({ variant: "rocm" }))).toBe("stable-diffusion.cpp (rocm)");
  });

  it("keeps the class-default star", () => {
    expect(backendOptionLabel(row({ variant: "vulkan", default: true }))).toBe("stable-diffusion.cpp (vulkan) ★");
  });

  it("falls back to the engine label for a row with no name", () => {
    expect(backendOptionLabel(row({ name: "", kind: "llama", variant: "vulkan" }))).toBe("llama.cpp (vulkan)");
    expect(backendOptionLabel(row({ name: "", kind: "sd" }))).toBe("sd-server");
    // An engine nobody has a label for still shows something.
    expect(backendOptionLabel(row({ name: "", kind: "weird" }))).toBe("weird");
  });

  it("ignores blank/whitespace variants", () => {
    expect(backendOptionLabel(row({ variant: "" }))).toBe("stable-diffusion.cpp");
    expect(backendOptionLabel(row({ variant: "  " }))).toBe("stable-diffusion.cpp");
  });
});

describe("backendOptionDetail", () => {
  it("leads with the version", () => {
    expect(backendOptionDetail(row({ variant: "rocm", version: "master-841-6b3edaa" }))).toBe("master-841-6b3edaa · sd-server");
  });

  it("is the engine label alone for a row with no version", () => {
    expect(backendOptionDetail(row({ kind: "llama" }))).toBe("llama.cpp");
  });
});

describe("backendPickOptions", () => {
  it("keeps plain labels unique and only versions the colliding ones", () => {
    // Three installed builds, the first one active: the two rocm versions would
    // both read "stable-diffusion.cpp (rocm)" in the closed trigger.
    const opts = backendPickOptions([
      row({ id: "vulkan", variant: "vulkan", version: "master-841-6b3edaa", default: true }),
      row({ id: "rocm841", variant: "rocm", version: "master-841-6b3edaa", build: true }),
      row({ id: "rocm849", variant: "rocm", version: "master-849-d04e895", build: true }),
    ]);
    expect(opts.map((o) => o.label)).toEqual([
      "stable-diffusion.cpp (vulkan) ★",
      "stable-diffusion.cpp (rocm) · master-841-6b3edaa",
      "stable-diffusion.cpp (rocm) · master-849-d04e895",
    ]);
    expect(opts.map((o) => o.value)).toEqual(["vulkan", "rocm841", "rocm849"]);
    expect(opts[1].detail).toBe("master-841-6b3edaa · sd-server");
  });

  it("falls back to the bare label when there is no version to add", () => {
    const opts = backendPickOptions([
      row({ id: "a", name: "same", path: "/a.exe" }),
      row({ id: "b", name: "same", path: "/b.exe" }),
    ]);
    expect(opts.map((o) => o.label)).toEqual(["same", "same"]);
  });
});

describe("hideDuplicateBuildRows", () => {
  const active = row({ id: "managed-sd-server", path: "C:/bin/sd-server/vulkan/sd-server.exe", variant: "vulkan", default: true });
  const activeBuild = row({
    id: "build-sd-server-841-vulkan",
    path: "C:/bin/sd-server/vulkan/sd-server.exe",
    variant: "vulkan",
    version: "841",
    build: true,
    managed: true,
  });
  const otherBuild = row({
    id: "build-sd-server-841-rocm",
    path: "C:/bin/sd-server/rocm/sd-server.exe",
    variant: "rocm",
    version: "841",
    build: true,
    managed: true,
  });

  it("hides the build row of whatever the active row already points at", () => {
    expect(hideDuplicateBuildRows([active, activeBuild, otherBuild]).map((r) => r.id)).toEqual([
      "managed-sd-server",
      "build-sd-server-841-rocm",
    ]);
  });

  it("matches paths across slash styles and case", () => {
    const windows = row({ id: "w", path: "C:\\Bin\\SD\\Sd-Server.exe" });
    const build = row({ id: "b", path: "c:/bin/sd/sd-server.exe", build: true });
    expect(hideDuplicateBuildRows([windows, build]).map((r) => r.id)).toEqual(["w"]);
  });

  it("keeps a pinned row even when it duplicates one", () => {
    expect(hideDuplicateBuildRows([active, activeBuild], activeBuild.id).map((r) => r.id)).toEqual([
      "managed-sd-server",
      "build-sd-server-841-vulkan",
    ]);
  });

  it("never hides non-build rows, and keeps them all when nothing matches", () => {
    const manual = row({ id: "mine", path: "D:/tools/sd.exe" });
    expect(hideDuplicateBuildRows([active, manual, otherBuild]).map((r) => r.id)).toEqual([
      "managed-sd-server",
      "mine",
      "build-sd-server-841-rocm",
    ]);
  });
});

describe("backend class taxonomy", () => {
  it("files audio.cpp under Audio, not Other", () => {
    for (const kind of ["audiocpp", "audio.cpp", "audiocpp-server", "AudioCpp"]) {
      expect(backendClass(kind)).toBe("audio");
    }
    expect(BACKEND_CLASSES.find((c) => c.id === "audio")?.label).toBe("Audio");
  });

  it("serves both speech classes from the one row", () => {
    expect(backendClasses("audiocpp")).toEqual(["tts", "asr"]);
    expect(backendServesClass("audiocpp", "tts")).toBe(true);
    expect(backendServesClass("audiocpp", "asr")).toBe(true);
    expect(backendServesClass("audiocpp", "llm")).toBe(false);
  });

  it("leaves the single-class engines exactly where they were", () => {
    expect(backendClass("llama")).toBe("llm");
    expect(backendClass("vllm")).toBe("llm");
    expect(backendClass("sd-server")).toBe("image");
    expect(backendClass("ttscpp")).toBe("tts");
    expect(backendClass("parakeet")).toBe("asr");
    expect(backendClass("sam3")).toBe("segment");
    expect(backendClass("trellis2")).toBe("3d");
    expect(backendClass("esrgan")).toBe("upscale");
  });

  it("keeps an unknown kind visible rather than dropping it", () => {
    expect(backendClass("brand-new-engine")).toBe("custom");
    expect(backendClasses("brand-new-engine")).toEqual([]);
    expect(backendServesClass("brand-new-engine", "llm")).toBe(false);
  });

  it("gives every class in the table a group it can be filed under", () => {
    for (const cls of BACKEND_CLASSES) {
      for (const eng of cls.engines) expect(backendClass(eng.kind)).toBe(cls.id);
    }
  });
});
