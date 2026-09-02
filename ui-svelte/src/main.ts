import "./index.css";
// KaTeX's stylesheet lives here rather than in index.css because index.css is
// shared with the first-run wizard's bundle (vite.setup.config.ts), and an
// @import there dragged ~60 math font files into a window that renders no math.
import "katex/dist/katex.min.css";
import "highlight.js/styles/github-dark.css";
import App from "./App.svelte";
import { mount } from "svelte";
import { isNative, installExternalLinkHandler, signalAppReady } from "./lib/native";
import { initUIScale } from "./stores/uiScale";

// Marks the document as running inside the app window, which index.css keys the
// title-bar height off. Set before mount so the first paint is already the right
// size -- flipping it afterwards makes every full-height view visibly resize.
//
// The wizard bundle deliberately does NOT set this: its title bar is part of its
// own h-screen root, so shortening h-screen there would subtract the bar twice.
if (isNative) document.documentElement.setAttribute("data-native", "");

// Applied before mount for the same reason as the attribute above: a scale set
// after the first paint makes the entire app visibly jump. Also binds
// Ctrl+Plus/Minus/0, which the app window has no browser chrome to provide.
initUIScale();

// Installed at the document, outside the component tree, so it covers the
// dashboard, the playground and every modal without each of them opting in.
// Never torn down: it lives as long as the page does.
installExternalLinkHandler();

const app = mount(App, {
  target: document.getElementById("app")!,
});

// After mount, so it means what the native window reads it as: the bundle
// parsed, ran, and produced a component tree. Anything that throws above this
// line leaves the window blank and the signal unsent, which is exactly the case
// the window's watchdog exists to recover from.
signalAppReady();

export default app;
