// Larik landing page: scroll reveals, install tabs, and the hero's dot field.
// The field is decoration: without canvas or with reduced motion the hero
// reads the same (reduced motion draws one still frame).
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

// Install tabs: click or arrow keys switch the panel.
const tabs = [...document.querySelectorAll('.lx-tabs [role="tab"]')];
const select = (tab) => {
  for (const t of tabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", on);
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  }
};
tabs.forEach((t, i) => {
  t.addEventListener("click", () => select(t));
  t.addEventListener("keydown", (e) => {
    const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = tabs[(i + step + tabs.length) % tabs.length];
    select(next);
    next.focus();
  });
});

// The hero field: rows of dots (larik is Indonesian for an array, a row)
// rolling toward a glowing horizon, violet in the troughs, accent on the crests.
function field(canvas) {
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  const ROWS = 96, COLS = 200;
  const rand = (s) => { const x = Math.sin(s * 12.9898) * 43758.5453; return x - Math.floor(x); };
  const jitter = new Float32Array(ROWS * COLS * 2);
  for (let i = 0; i < jitter.length; i++) jitter[i] = rand(i + 1) - 0.5;
  const stars = Array.from({ length: 320 }, (_, i) => ({ x: rand(i * 3 + 7), y: rand(i * 5 + 11), a: 0.15 + rand(i * 7 + 13) * 0.5, s: 0.5 + rand(i * 11 + 17) * 2 }));
  const rgb = (hex) => { const n = parseInt(hex.trim().replace("#", "").slice(0, 6), 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255]; };
  const acc = rgb(getComputedStyle(document.documentElement).getPropertyValue("--accent") || "#ff6ac1");
  const violet = [124, 92, 255], rim = [120, 170, 255];
  let raf = 0, visible = true;

  const draw = (t) => {
    const dpr = Math.min(devicePixelRatio || 1, 2);
    const w = canvas.clientWidth, h = canvas.clientHeight;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.globalAlpha = 1;
    ctx.fillStyle = "#0a090d";
    ctx.fillRect(0, 0, w, h);

    ctx.fillStyle = "#edebf2";
    stars.forEach((s, i) => {
      ctx.globalAlpha = s.a * (0.55 + 0.45 * Math.sin(t * 0.0012 * s.s + i));
      ctx.fillRect(s.x * w, s.y * h * 0.62, 1, 1);
    });

    // On narrow screens the copy sits on top, so the wave moves down and centres.
    const narrow = w < 760;
    const horizon = h * (narrow ? 0.6 : 0.34), f = h * (narrow ? 0.3 : 0.42), cam = 1.5, cx = w * (narrow ? 0.5 : 0.66);
    const glow = ctx.createLinearGradient(0, horizon - 40, 0, horizon + 60);
    glow.addColorStop(0, `rgba(${rim},0)`);
    glow.addColorStop(0.45, `rgba(${rim},0.16)`);
    glow.addColorStop(1, `rgba(${acc},0)`);
    ctx.globalAlpha = 1;
    ctx.fillStyle = glow;
    ctx.fillRect(0, horizon - 40, w, 100);

    const T = t * 0.001;
    for (let r = ROWS - 1; r >= 0; r--) {
      const z = 1.1 + r * 0.13;
      const depth = 1 - r / ROWS;
      const span = (w / f) * z * 0.85;
      const base = r > ROWS * 0.8 ? rim : violet;
      for (let k = 0; k < COLS; k++) {
        const j = (r * COLS + k) * 2;
        const X = (k / (COLS - 1) - 0.5) * 2 * span + jitter[j] * span * 0.012;
        const sx = cx + (X * f) / z;
        if (sx < -4 || sx > w + 4) continue;
        const Y = 0.38 * Math.sin(X * 0.8 + T * 0.6) + 0.26 * Math.sin(z * 0.62 - T * 0.9) + 0.14 * Math.sin((X + z) * 1.7 + T * 1.3);
        const sy = horizon + ((cam - Y) * f) / z + jitter[j + 1] * 2;
        const crest = Math.max(0, Math.min(1, (Y + 0.7) / 1.4));
        const m = crest * depth;
        ctx.globalAlpha = Math.min(1, Math.pow(depth, 0.35) * (0.4 + 0.8 * crest));
        ctx.fillStyle = `rgb(${Math.round(base[0] + (acc[0] - base[0]) * m)},${Math.round(base[1] + (acc[1] - base[1]) * m)},${Math.round(base[2] + (acc[2] - base[2]) * m)})`;
        const size = Math.max(0.9, 2.4 * depth);
        ctx.fillRect(sx, sy, size, size);
      }
    }
    raf = !reduced && visible && !document.hidden ? requestAnimationFrame(draw) : 0;
  };

  const resume = () => { if (!raf && visible && !document.hidden) raf = requestAnimationFrame(draw); };
  if (reduced) {
    draw(0);
    addEventListener("resize", () => draw(0), { passive: true });
    return;
  }
  // Pause off screen and in background tabs.
  new IntersectionObserver(([e]) => { visible = e.isIntersecting; resume(); }).observe(canvas);
  document.addEventListener("visibilitychange", resume);
  resume();
}

const canvas = document.querySelector(".lx-field");
if (canvas) field(canvas);
