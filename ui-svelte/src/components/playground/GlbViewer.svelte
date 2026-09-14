<script lang="ts">
  import { onDestroy } from "svelte";
  import { RotateCcw } from "lucide-svelte";
  import { tip } from "../../lib/tooltip";

  // An orbitable view of one GLB.
  //
  // three.js is loaded with a dynamic import, the same way lib/diagrams.ts loads
  // mermaid: it is by far the heaviest dependency in the bundle and only this
  // tab needs it, so it must not sit on the critical path of a dashboard that
  // never opens a mesh.
  //
  // DISPOSAL IS LOAD-BEARING HERE, not hygiene. A default TRELLIS.2 mesh is
  // about a million triangles with a 1024 texture atlas, and those buffers live
  // in GPU memory, which the JS garbage collector does not manage: dropping the
  // last reference to a Mesh frees the JS object and leaks the VBO. A thread of
  // ten turns would then hold ten meshes' worth of GPU memory on a card that is
  // simultaneously being asked to run the model. Every geometry, material and
  // texture is therefore disposed explicitly, and so is the renderer's context.

  let { src, alt = "Generated mesh" }: { src: string; alt?: string } = $props();

  let host = $state<HTMLDivElement>();
  let loading = $state(true);
  let error = $state("");

  // The teardown for whatever is currently mounted. Held outside the effect so
  // both a src change and an unmount run the same path.
  let teardown: (() => void) | null = null;
  // Bumped by the reset button to re-run the effect and re-frame the camera.
  let resetToken = $state(0);

  $effect(() => {
    const url = src;
    void resetToken;
    const el = host;
    if (!el || !url) return;

    let cancelled = false;
    loading = true;
    error = "";

    void mount(el, url, () => cancelled).catch((e) => {
      if (cancelled) return;
      error = e instanceof Error ? e.message : String(e);
      loading = false;
    });

    return () => {
      cancelled = true;
      teardown?.();
      teardown = null;
    };
  });

  onDestroy(() => {
    teardown?.();
    teardown = null;
  });

  async function mount(el: HTMLDivElement, url: string, cancelled: () => boolean) {
    const [THREE, { GLTFLoader }, { OrbitControls }, { RoomEnvironment }] = await Promise.all([
      import("three"),
      import("three/examples/jsm/loaders/GLTFLoader.js"),
      import("three/examples/jsm/controls/OrbitControls.js"),
      import("three/examples/jsm/environments/RoomEnvironment.js"),
    ]);
    if (cancelled()) return;

    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    // Cap at 2: a mesh viewer at a 3x device ratio quadruples the fragment cost
    // for a difference nobody sees, on the same GPU that is running the model.
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    renderer.toneMapping = THREE.ACESFilmicToneMapping;
    renderer.toneMappingExposure = 1;
    el.appendChild(renderer.domElement);
    renderer.domElement.style.display = "block";
    renderer.domElement.style.width = "100%";
    renderer.domElement.style.height = "100%";
    // Without this the canvas swallows vertical scroll: a wheel over the mesh
    // zooms (which is right) but a touch drag would otherwise stop the thread
    // scrolling past it on a phone.
    renderer.domElement.style.touchAction = "none";

    const scene = new THREE.Scene();

    // Image-based lighting rather than a light rig. TRELLIS.2 bakes a PBR
    // material with real metalness/roughness, and those are lit almost entirely
    // by the environment map: under two directional lights a metallic surface
    // renders black. RoomEnvironment is a procedural scene, so this costs no
    // asset and cannot 404 in an offline install.
    const pmrem = new THREE.PMREMGenerator(renderer);
    const envRT = pmrem.fromScene(new RoomEnvironment(), 0.04);
    scene.environment = envRT.texture;

    const camera = new THREE.PerspectiveCamera(45, 1, 0.01, 1000);
    const controls = new OrbitControls(camera, renderer.domElement);
    controls.enableDamping = true;
    controls.dampingFactor = 0.08;
    // Pan is off on purpose: with damping and zoom already bound to the two
    // gestures people try, a third that slides the subject out of frame is the
    // one that gets triggered by accident and leaves a blank viewport.
    controls.enablePan = false;

    const loader = new GLTFLoader();
    const gltf = await loader.loadAsync(url).catch(() => {
      throw new Error("That mesh could not be decoded.");
    });
    if (cancelled()) {
      renderer.dispose();
      envRT.dispose();
      pmrem.dispose();
      return;
    }

    const root = gltf.scene;
    scene.add(root);

    // Frame the subject. A GLB arrives at whatever scale and offset it was
    // authored in, so a fixed camera position shows either the inside of the
    // model or an empty world: the bounding sphere is the only reliable way to
    // place it. Re-centred at the origin so orbiting turns the subject rather
    // than swinging it around a distant pivot.
    const box = new THREE.Box3().setFromObject(root);
    const sphere = box.getBoundingSphere(new THREE.Sphere());
    root.position.sub(sphere.center);
    const dist = sphere.radius / Math.sin((camera.fov * Math.PI) / 360);
    camera.position.set(0, sphere.radius * 0.25, dist * 1.25);
    camera.near = Math.max(dist / 1000, 0.001);
    camera.far = dist * 100;
    camera.updateProjectionMatrix();
    controls.target.set(0, 0, 0);
    controls.minDistance = sphere.radius * 0.5;
    controls.maxDistance = dist * 6;
    controls.update();

    // Follow the container instead of the window: this canvas sits in a chat
    // bubble whose width changes with the history flyout and the app window's
    // own tab strip, neither of which fires a window resize.
    const ro = new ResizeObserver(() => {
      const w = el.clientWidth || 1;
      const h = el.clientHeight || 1;
      renderer.setSize(w, h, false);
      camera.aspect = w / h;
      camera.updateProjectionMatrix();
    });
    ro.observe(el);

    // Damping needs a frame loop, but an idle mesh does not need to be redrawn
    // sixty times a second on a box that is mid-generation: controls.update()
    // reports whether anything actually moved, and a still frame costs one
    // function call.
    let raf = 0;
    let dirty = true;
    controls.addEventListener("change", () => (dirty = true));
    const tick = () => {
      raf = requestAnimationFrame(tick);
      const moved = controls.update();
      if (moved || dirty) {
        renderer.render(scene, camera);
        dirty = moved;
      }
    };
    tick();

    loading = false;

    teardown = () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
      controls.dispose();
      envRT.dispose();
      pmrem.dispose();
      scene.traverse((obj) => {
        const mesh = obj as unknown as { geometry?: { dispose(): void }; material?: unknown };
        mesh.geometry?.dispose();
        for (const m of [mesh.material].flat()) {
          const mat = m as unknown as Record<string, unknown> & { dispose?: () => void };
          if (!mat?.dispose) continue;
          // Every texture slot a glTF material can carry. Disposing the material
          // alone leaves the atlas resident, and the atlas is the big one.
          for (const v of Object.values(mat)) {
            const tex = v as { isTexture?: boolean; dispose?: () => void };
            if (tex?.isTexture) tex.dispose?.();
          }
          mat.dispose();
        }
      });
      renderer.dispose();
      // The context itself, not just its resources: browsers cap live WebGL
      // contexts (typically 16) and silently kill the OLDEST one when a new
      // viewer asks past the limit, which would blank a mesh further up the
      // thread. Scrolling a long thread makes that reachable.
      renderer.forceContextLoss();
      renderer.domElement.remove();
    };
  }
</script>

<div class="relative w-full aspect-square max-h-72 rounded-xl overflow-hidden border border-card-border bg-secondary">
  <div bind:this={host} class="absolute inset-0" role="img" aria-label={alt}></div>

  {#if loading && !error}
    <div class="absolute inset-0 flex items-center justify-center gap-2 text-txtsecondary text-xs pointer-events-none">
      <span class="inline-block w-4 h-4 border-2 border-primary border-t-transparent rounded-full animate-spin"></span>
      Loading mesh…
    </div>
  {/if}

  {#if error}
    <div class="absolute inset-0 flex items-center justify-center p-3 text-center text-xs text-red-500">{error}</div>
  {:else if !loading}
    <button
      class="absolute bottom-1.5 right-1.5 p-1 rounded bg-black/40 text-white/80 hover:text-white hover:bg-black/60 transition-colors"
      onclick={() => (resetToken += 1)}
      use:tip={"Reset view"}
      aria-label="Reset view"
    >
      <RotateCcw class="w-3.5 h-3.5" />
    </button>
  {/if}
</div>
