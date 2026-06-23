<script lang="ts">
  import ModelsPanel from "../components/ModelsPanel.svelte";
  import type { ModelCategory } from "../lib/modelUtils";

  // svelte-spa-router passes named route params; /models (no param) → llm.
  let { params }: { params?: { category?: ModelCategory } } = $props();
  let category = $derived(params?.category ?? "llm");
</script>

<!-- The Models page now owns its own two-section layout (active panel + card
     grid); logs live inside the active panel, so no outer split here. -->
<ModelsPanel {category} />
