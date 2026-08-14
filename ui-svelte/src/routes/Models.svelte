<script lang="ts">
  import ModelsPanel from "../components/ModelsPanel.svelte";
  import type { ModelCategory } from "../lib/modelUtils";

  // svelte-spa-router passes named route params; /models (no param) → llm.
  let { params }: { params?: { category?: ModelCategory } } = $props();
  let category = $derived(params?.category ?? "llm");
</script>

<!-- One table page: the panel owns the category tabs, filter bar and table.
     Live models live on the Dashboard, so nothing is duplicated here. -->
<ModelsPanel {category} />
