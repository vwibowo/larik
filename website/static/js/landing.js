// Larik landing page: scroll reveals, pointer glow, and two three.js scenes
// (a wire harness in the hero, a glowing glass sandbox further down).
// Every scene is decoration: if WebGL or the CDN is unavailable, the CSS
// fallback gradients stay and the page reads the same.
const THREE_URL = "https://cdn.jsdelivr.net/npm/three@0.170.0/build/three.module.min.js";
const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
const body = document.body;

// Top bar turns solid once the hero scrolls away.
const onScroll = () => body.classList.toggle("scrolled", scrollY > 40);
addEventListener("scroll", onScroll, { passive: true });
onScroll();

// Reveal on scroll. The terminal session also waits until it is on screen.
body.classList.add("js-reveal");
const revealer = new IntersectionObserver((entries) => {
  for (const e of entries) {
    if (e.isIntersecting) {
      e.target.classList.add("in");
      revealer.unobserve(e.target);
    }
  }
}, { rootMargin: "0px 0px -8% 0px", threshold: 0.12 });
document.querySelectorAll(".reveal, .kind-landing .term").forEach((el) => revealer.observe(el));

// Feature tiles: the teal edge follows the pointer.
document.querySelectorAll(".lx-feature").forEach((el) => {
  el.addEventListener("pointermove", (e) => {
    const r = el.getBoundingClientRect();
    el.style.setProperty("--mx", `${e.clientX - r.left}px`);
    el.style.setProperty("--my", `${e.clientY - r.top}px`);
  });
});

function webgl() {
  try {
    const c = document.createElement("canvas");
    return !!(c.getContext("webgl2") || c.getContext("webgl"));
  } catch {
    return false;
  }
}

if (webgl()) {
  import(THREE_URL).then((THREE) => {
    const canvases = document.querySelectorAll("canvas[data-scene]");
    for (const canvas of canvases) {
      const make = { harness: harnessScene, box: boxScene }[canvas.dataset.scene];
      if (make) mount(THREE, canvas, make);
    }
  }).catch(() => {});
}

// mount wires a scene to its canvas: sizing, a render loop that runs only
// while the canvas is on screen, and a single still frame for reduced motion.
function mount(THREE, canvas, make) {
  // Opaque black: additive glows would otherwise leave grey halos in a transparent canvas.
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, powerPreference: "high-performance" });
  renderer.setClearColor(0x000000, 1);
  renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.05;
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(40, 1, 0.1, 200);
  scene.environment = studio(THREE, renderer);
  const tick = make(THREE, scene, camera, canvas);

  const resize = () => {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  };
  new ResizeObserver(() => { resize(); if (reduced) draw(0); }).observe(canvas);
  resize();

  const draw = (t) => { tick(t / 1000); renderer.render(scene, camera); };
  let raf = 0, visible = false;
  const loop = (t) => { draw(t); raf = requestAnimationFrame(loop); };
  new IntersectionObserver(([e]) => {
    visible = e.isIntersecting;
    if (reduced) { if (visible) draw(4000); return; }
    cancelAnimationFrame(raf);
    if (visible) raf = requestAnimationFrame(loop);
  }).observe(canvas);
  draw(reduced ? 4000 : 0);
  requestAnimationFrame(() => canvas.classList.add("ready"));
}

// studio builds a reflection map from a dark room with a few softbox strips,
// which is what gives the metal its bright chrome highlights.
function studio(THREE, renderer) {
  const room = new THREE.Scene();
  room.background = new THREE.Color(0x020203);
  const box = (w, h, x, y, z, color, intensity) => {
    const m = new THREE.Mesh(new THREE.PlaneGeometry(w, h), new THREE.MeshBasicMaterial({ color: new THREE.Color(color).multiplyScalar(intensity), side: THREE.DoubleSide }));
    m.position.set(x, y, z);
    m.lookAt(0, 0, 0);
    room.add(m);
  };
  box(14, 2, 0, 9, 0, 0xffffff, 3.2);
  box(2, 10, -9, 3, 2, 0xffffff, 1.4);
  box(2, 10, 9, 2, -3, 0xd9fff9, 1.2);
  box(10, 1.2, 0, 4, -9, 0x5eead4, 0.7);
  box(8, 1, 0, -6, 8, 0xffffff, 0.35);
  const pmrem = new THREE.PMREMGenerator(renderer);
  const env = pmrem.fromScene(room, 0.03).texture;
  pmrem.dispose();
  return env;
}

// harnessScene: a wire harness. Strands from many models (left, deep) are
// pinched through one chrome collar (Larik) and fan out as tool calls, with
// teal pulses riding them through.
function harnessScene(THREE, scene, camera, canvas) {
  const group = new THREE.Group();
  scene.add(group);
  const V = (x, y, z) => new THREE.Vector3(x, y, z);
  const rand = (a, b) => a + Math.random() * (b - a);
  const disc = (r0, r1) => { const a = rand(0, Math.PI * 2), r = rand(r0, r1); return [Math.cos(a) * r, Math.sin(a) * r]; };

  // Strands run along x: inputs at -10, the collar at 0, five tool clusters at +8.
  const clusters = [0, 1, 2, 3, 4].map((i) => (i / 5) * Math.PI * 2 + 0.4);
  const chrome = new THREE.MeshPhysicalMaterial({ color: 0xffffff, metalness: 1, roughness: 0.12, clearcoat: 1, envMapIntensity: 1.5 });
  const tealMetal = new THREE.MeshPhysicalMaterial({ color: 0x5eead4, emissive: 0x2dd4bf, emissiveIntensity: 0.55, metalness: 0.9, roughness: 0.2, envMapIntensity: 1.3 });
  // A wide, faint additive sheath around teal strands reads as a glow without a post-processing pass.
  const sheath = new THREE.MeshBasicMaterial({ color: 0x2dd4bf, transparent: true, opacity: 0.16, blending: THREE.AdditiveBlending, depthWrite: false });
  const curves = [];
  for (let i = 0; i < 36; i++) {
    const [sy, sz] = disc(1.6, 4);
    const [my, mz] = disc(0, 0.28);
    const c = clusters[i % 5] + rand(-0.18, 0.18);
    const cy = Math.sin(c), cz = Math.cos(c), spread = rand(2.4, 3.6);
    const curve = new THREE.CatmullRomCurve3([
      V(-11, sy, sz), V(-5, sy * 0.4, sz * 0.4), V(-1.6, my * 1.4, mz * 1.4), V(0, my, mz),
      V(1.6, my * 1.4, mz * 1.4), V(4.2, cy * 1.1, cz * 1.1), V(8, cy * spread, cz * spread),
    ]);
    curves.push(curve);
    const r = i % 6 === 0 ? 0.022 : rand(0.009, 0.017);
    group.add(new THREE.Mesh(new THREE.TubeGeometry(curve, 180, r, 6), i % 6 === 0 ? tealMetal : chrome));
    if (i % 6 === 0) group.add(new THREE.Mesh(new THREE.TubeGeometry(curve, 120, r * 4, 8), sheath));
  }

  // The collar: a thick chrome ring, two open arcs spinning around it, a teal glow inside.
  const collar = new THREE.Group();
  collar.rotation.y = Math.PI / 2; // torus axis along x
  const ringMat = new THREE.MeshPhysicalMaterial({ color: 0xffffff, metalness: 1, roughness: 0.06, clearcoat: 1, envMapIntensity: 1.8 });
  collar.add(new THREE.Mesh(new THREE.TorusGeometry(0.72, 0.16, 48, 160), ringMat));
  const arcA = new THREE.Mesh(new THREE.TorusGeometry(1.02, 0.028, 16, 220, Math.PI * 1.65), ringMat);
  const arcB = new THREE.Mesh(new THREE.TorusGeometry(1.16, 0.016, 12, 220, Math.PI * 0.7), tealMetal);
  collar.add(arcA, arcB);
  group.add(collar);
  const halo = glowTexture(THREE);
  const glow = new THREE.Sprite(new THREE.SpriteMaterial({ map: halo, color: 0x2dd4bf, transparent: true, opacity: 0.55, blending: THREE.AdditiveBlending, depthWrite: false }));
  glow.scale.setScalar(3.2);
  group.add(glow);
  const collarLight = new THREE.PointLight(0x5eead4, 16, 5, 1.5);
  group.add(collarLight);

  // Pulses: requests travelling in, through the collar, out to a tool.
  const pulses = [];
  for (let i = 0; i < 12; i++) {
    const sp = new THREE.Sprite(new THREE.SpriteMaterial({ map: halo, color: 0x7ff5e3, transparent: true, blending: THREE.AdditiveBlending, depthWrite: false }));
    sp.scale.setScalar(rand(0.5, 0.8));
    group.add(sp);
    const light = i < 3 ? new THREE.PointLight(0x2dd4bf, 9, 3, 1.5) : null;
    if (light) group.add(light);
    pulses.push({ sp, light, curve: curves[i * 3 % curves.length], off: i / 12, speed: rand(0.05, 0.08), last: 0 });
  }

  const key = new THREE.DirectionalLight(0xffffff, 1.5);
  key.position.set(-2, 6, 6);
  scene.add(key, new THREE.AmbientLight(0xffffff, 0.04));
  scene.fog = new THREE.Fog(0x000000, 10, 20);

  const pointer = { x: 0, y: 0 };
  addEventListener("pointermove", (e) => { pointer.x = e.clientX / innerWidth - 0.5; pointer.y = e.clientY / innerHeight - 0.5; }, { passive: true });
  camera.position.set(0, 0.6, 13);
  camera.lookAt(0, 0, 0);

  return (t) => {
    const narrow = innerWidth < 860;
    camera.fov = narrow ? 52 : 38;
    camera.updateProjectionMatrix();
    // Deep end behind the headline, collar beside it, fan-out toward the viewer.
    group.position.set(narrow ? 0.6 : 3.7, (narrow ? -2.6 : 0.1) + Math.min(scrollY, 900) * 0.0025, 0);
    group.scale.setScalar(narrow ? 0.72 : 1);
    group.rotation.set(0.08 + pointer.y * 0.08, -1.0 + Math.sin(t * 0.15) * 0.06 + pointer.x * 0.14, -0.14 + Math.sin(t * 0.11) * 0.03);
    arcA.rotation.z = t * 0.35;
    arcB.rotation.z = -t * 0.6 + 1;
    glow.material.opacity = 0.7 + Math.sin(t * 1.3) * 0.12;
    for (const p of pulses) {
      const raw = (t * p.speed + p.off) % 1;
      if (raw < p.last) p.curve = curves[Math.floor(Math.random() * curves.length)];
      p.last = raw;
      const u = raw - 0.06 * Math.sin(raw * Math.PI * 2); // quicker through the collar
      p.curve.getPointAt(Math.min(Math.max(u, 0), 1), p.sp.position);
      const fade = Math.min(raw / 0.12, (1 - raw) / 0.15, 1);
      p.sp.material.opacity = fade;
      if (p.light) { p.light.position.copy(p.sp.position); p.light.intensity = 9 * fade; }
    }
  };
}

// boxScene: the sandbox. A glass cube with a glowing teal core; three small
// primitives (your models) orbit it and link to the core as they pass.
function boxScene(THREE, scene, camera, canvas) {
  const group = new THREE.Group();
  scene.add(group);
  const halo = glowTexture(THREE);
  const additive = (color, opacity) => new THREE.SpriteMaterial({ map: halo, color, transparent: true, opacity, blending: THREE.AdditiveBlending, depthWrite: false });

  // Glass cube with chrome edges.
  const S = 2.6;
  const box = new THREE.Group();
  group.add(box);
  const glass = new THREE.Mesh(new THREE.BoxGeometry(S, S, S), new THREE.MeshPhysicalMaterial({
    // Plain transparency rather than transmission: refraction would split the core and hide its glow.
    color: 0xbff7ee, metalness: 0.2, roughness: 0.05, transparent: true, opacity: 0.14, clearcoat: 1,
    envMapIntensity: 2, side: THREE.DoubleSide, depthWrite: false,
  }));
  box.add(glass);
  const chrome = new THREE.MeshPhysicalMaterial({ color: 0xffffff, metalness: 1, roughness: 0.08, clearcoat: 1, envMapIntensity: 1.7 });
  const edge = new THREE.CylinderGeometry(0.035, 0.035, S + 0.07, 10);
  const h = S / 2;
  for (const axis of ["x", "y", "z"]) {
    for (const a of [-h, h]) for (const b of [-h, h]) {
      const m = new THREE.Mesh(edge, chrome);
      if (axis === "x") { m.rotation.z = Math.PI / 2; m.position.set(0, a, b); }
      if (axis === "y") m.position.set(a, 0, b);
      if (axis === "z") { m.rotation.x = Math.PI / 2; m.position.set(a, b, 0); }
      box.add(m);
    }
  }

  // The glowing core.
  const core = new THREE.Mesh(new THREE.OctahedronGeometry(0.62, 0), new THREE.MeshPhysicalMaterial({
    color: 0x5eead4, emissive: 0x2dd4bf, emissiveIntensity: 1.2, metalness: 0.3, roughness: 0.2, clearcoat: 1, flatShading: true,
  }));
  const coreGlow = new THREE.Sprite(additive(0x2dd4bf, 0.8));
  coreGlow.scale.setScalar(4.2);
  const coreLight = new THREE.PointLight(0x5eead4, 22, 8, 1.4);
  group.add(core, coreGlow, coreLight);

  // Three orbiting models, each with a dotted light link to the core.
  const rimmed = new THREE.MeshPhysicalMaterial({ color: 0xffffff, metalness: 1, roughness: 0.1, clearcoat: 1, emissive: 0x2dd4bf, emissiveIntensity: 0.25, envMapIntensity: 1.7 });
  const orbiters = [
    new THREE.SphereGeometry(0.34, 48, 32),
    new THREE.TorusGeometry(0.3, 0.12, 32, 80),
    new THREE.TetrahedronGeometry(0.42, 0),
  ].map((geo, i) => {
    const mesh = new THREE.Mesh(geo, rimmed);
    const glow = new THREE.Sprite(additive(0x5eead4, 0.35));
    glow.scale.setScalar(1.3);
    const dots = Array.from({ length: 7 }, () => { const d = new THREE.Sprite(additive(0x9ffbee, 0)); d.scale.setScalar(0.28); return d; });
    group.add(mesh, glow, ...dots);
    return { mesh, glow, dots, rx: 3.3 + i * 0.35, rz: 2.4 + i * 0.3, tilt: -0.5 + i * 0.5, speed: 0.22 - i * 0.04, phase: i * 2.1 };
  });

  const key = new THREE.DirectionalLight(0xffffff, 2);
  key.position.set(-3, 6, 5);
  scene.add(key, new THREE.AmbientLight(0xffffff, 0.05));

  camera.position.set(0, 1.4, 11);
  camera.lookAt(0, 0, 0);
  const section = canvas.parentElement;
  const tmp = new THREE.Vector3();

  return (t) => {
    const narrow = innerWidth < 860;
    group.scale.setScalar(narrow ? 0.62 : 0.82);
    const r = section.getBoundingClientRect();
    const p = (innerHeight - r.top) / (innerHeight + r.height) - 0.5; // -0.5 → 0.5 through the section
    box.rotation.set(0.45 + p * 0.4, t * 0.18 + p * 1.2, 0.2);
    core.rotation.set(t * 0.4, t * 0.6, 0);

    let flare = 0;
    for (const o of orbiters) {
      const a = t * o.speed + o.phase + p * 1.5;
      o.mesh.position.set(Math.cos(a) * o.rx, Math.sin(a) * o.rx * Math.sin(o.tilt) * 0.5, Math.sin(a) * o.rz);
      o.mesh.rotation.set(t * 0.7, t * 0.5, 0);
      o.glow.position.copy(o.mesh.position);
      // Link strength peaks when the model swings in front of the box.
      const link = Math.max(0, Math.sin(a)) ** 3;
      flare = Math.max(flare, link);
      o.dots.forEach((d, i) => {
        const f = (i + 1) / (o.dots.length + 1);
        const travel = (f + t * 0.8) % 1;
        tmp.copy(o.mesh.position).multiplyScalar(1 - travel);
        d.position.copy(tmp);
        d.material.opacity = link * Math.sin(travel * Math.PI) * 0.9;
      });
    }
    const breathe = 1 + Math.sin(t * 1.6) * 0.06 + flare * 0.12;
    core.scale.setScalar(breathe);
    coreGlow.material.opacity = 0.6 + flare * 0.35;
    coreGlow.scale.setScalar(4.2 * breathe);
    coreLight.intensity = 18 + flare * 14;
  };
}

// glowTexture: a white radial falloff. Built as raw RGBA rather than drawn on
// a 2D canvas: canvases store premultiplied alpha, and unpremultiplying the
// faint tail on upload leaves noisy colour in it (dark, blue and yellow specks
// once the sprite is added to the scene).
function glowTexture(THREE) {
  const N = 128;
  const stops = [
    [0, 1],
    [0.18, 0.8],
    [0.45, 0.18],
    [1, 0],
  ];
  const data = new Uint8Array(N * N * 4);
  for (let y = 0; y < N; y++) {
    for (let x = 0; x < N; x++) {
      const r = Math.min(Math.hypot(x + 0.5 - N / 2, y + 0.5 - N / 2) / (N / 2), 1);
      let i = 1;
      while (stops[i][0] < r) i++;
      const [r0, a0] = stops[i - 1];
      const [r1, a1] = stops[i];
      const k = (y * N + x) * 4;
      data[k] = data[k + 1] = data[k + 2] = 255;
      data[k + 3] = Math.round((a0 + ((a1 - a0) * (r - r0)) / (r1 - r0)) * 255);
    }
  }
  const tex = new THREE.DataTexture(data, N, N, THREE.RGBAFormat);
  tex.colorSpace = THREE.SRGBColorSpace;
  tex.minFilter = tex.magFilter = THREE.LinearFilter;
  tex.generateMipmaps = false;
  tex.needsUpdate = true;
  return tex;
}
