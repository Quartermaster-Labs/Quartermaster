import "./index.css";
// KaTeX's stylesheet lives here rather than in index.css because index.css is
// shared with the first-run wizard's bundle (vite.setup.config.ts), and an
// @import there dragged ~60 math font files into a window that renders no math.
import "katex/dist/katex.min.css";
import "highlight.js/styles/github-dark.css";
import App from "./App.svelte";
import { mount } from "svelte";

const app = mount(App, {
  target: document.getElementById("app")!,
});

export default app;
