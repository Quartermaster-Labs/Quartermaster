// Entry point for the first-run wizard bundle (internal/setup/ui_dist).
//
// Deliberately thin: no router, no stores, no service layer. The wizard is one
// component with a linear flow, and the reason it is a second bundle at all is
// that it must not drag in the dashboard's chart/markdown/katex weight to draw
// three steps.
import "../index.css";
import SetupApp from "./SetupApp.svelte";
import { mount } from "svelte";

const app = mount(SetupApp, {
  target: document.getElementById("app")!,
});

export default app;
