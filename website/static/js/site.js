// Larik site: copy buttons, docs sidebar and TOC, download OS detection, search.
(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const base = document.body.dataset.base || "";

  // Copy buttons: data-copy, or the code block they belong to.
  document.addEventListener("click", async (e) => {
    const btn = e.target.closest(".copy");
    if (!btn) return;
    const text = btn.dataset.copy ?? btn.closest(".code")?.querySelector("pre")?.innerText ?? "";
    try {
      await navigator.clipboard.writeText(text.replace(/\n$/, ""));
      btn.textContent = "Copied";
      btn.classList.add("done");
    } catch {
      btn.textContent = "Press ⌘C";
    }
    setTimeout(() => { btn.textContent = "Copy"; btn.classList.remove("done"); }, 1600);
  });

  // Docs sidebar on small screens.
  const toggle = $(".side-toggle");
  const sidebar = $("#sidebar");
  if (toggle && sidebar) {
    const set = (open) => { sidebar.classList.toggle("open", open); toggle.setAttribute("aria-expanded", String(open)); };
    toggle.addEventListener("click", () => set(!sidebar.classList.contains("open")));
    document.addEventListener("click", (e) => {
      if (sidebar.classList.contains("open") && !sidebar.contains(e.target) && !toggle.contains(e.target)) set(false);
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") set(false); });
    const cur = $("#sidebar [aria-current]");
    if (cur && cur.offsetTop + cur.offsetHeight > sidebar.clientHeight) {
      sidebar.scrollTop = cur.offsetTop - sidebar.clientHeight / 2;
    }
  }

  // Diagrams: click to view at full size.
  document.addEventListener("click", (e) => {
    const fig = e.target.closest(".diagram");
    if (fig) { fig.classList.toggle("zoomed"); document.body.style.overflow = fig.classList.contains("zoomed") ? "hidden" : ""; }
  });
  document.addEventListener("keydown", (e) => {
    const fig = $(".diagram.zoomed");
    if (e.key === "Escape" && fig) { fig.classList.remove("zoomed"); document.body.style.overflow = ""; }
  });

  // Highlight the TOC entry for the section being read.
  const tocLinks = $$(".toc a");
  if (tocLinks.length && "IntersectionObserver" in window) {
    const byId = new Map(tocLinks.map((a) => [decodeURIComponent(a.hash.slice(1)), a]));
    const heads = [...byId.keys()].map((id) => document.getElementById(id)).filter(Boolean);
    const visible = new Set();
    const io = new IntersectionObserver((entries) => {
      for (const en of entries) en.isIntersecting ? visible.add(en.target) : visible.delete(en.target);
      let current = heads.find((h) => visible.has(h));
      if (!current) current = [...heads].reverse().find((h) => h.getBoundingClientRect().top < 120);
      tocLinks.forEach((a) => a.classList.remove("active"));
      if (current) byId.get(current.id)?.classList.add("active");
    }, { rootMargin: "-64px 0px -65% 0px" });
    heads.forEach((h) => io.observe(h));
  }

  // Download: mark the card for this system.
  const cards = $$(".dl-card");
  if (cards.length) {
    const ua = navigator.userAgent;
    const plat = (navigator.userAgentData?.platform || navigator.platform || "").toLowerCase();
    const os = /win/.test(plat) || /Windows/.test(ua) ? "windows" : /mac/.test(plat) || /Mac OS/.test(ua) ? "macos" : /linux/.test(plat) || /Linux/.test(ua) ? "linux" : "";
    const pick = (arch) => cards.find((c) => c.dataset.os === os && (!arch || c.dataset.arch === arch));
    const mark = (arch) => { cards.forEach((c) => c.classList.remove("match")); pick(arch)?.classList.add("match"); };
    if (os && !/Android|iPhone|iPad/.test(ua)) {
      if (os !== "macos") mark(/aarch64|arm64/i.test(ua) ? "arm64" : "amd64");
      else if (/aarch64|arm64/i.test(ua)) mark("arm64");
      else if (/Intel Mac/i.test(ua)) mark("amd64");
      navigator.userAgentData?.getHighEntropyValues?.(["architecture"]).then(({ architecture }) => {
        if (architecture) mark(architecture === "arm" ? "arm64" : "amd64");
      }).catch(() => {});
    }
  }

  // Search over search-index.json, loaded on first open.
  const box = $(".search");
  const input = $(".search input");
  const list = $(".search-results");
  let index = null, sel = 0, lastFocus = null;

  const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  const hl = (s, terms) => {
    let out = esc(s);
    for (const t of terms) out = out.replace(new RegExp(`(${t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")})`, "gi"), "<mark>$1</mark>");
    return out;
  };
  const snippet = (text, term) => {
    const i = text.toLowerCase().indexOf(term);
    if (i < 0) return text.slice(0, 120);
    const start = Math.max(0, i - 40);
    return (start ? "…" : "") + text.slice(start, i + 90);
  };

  const run = () => {
    const q = input.value.trim().toLowerCase();
    const terms = q.split(/\s+/).filter(Boolean);
    if (!index || !terms.length) { list.innerHTML = ""; return; }
    const results = [];
    for (const page of index) {
      const title = page.t.toLowerCase();
      const text = page.x.toLowerCase();
      if (!terms.every((t) => title.includes(t) || text.includes(t) || page.h.some(([, h]) => h.toLowerCase().includes(t)))) continue;
      let score = 0;
      for (const t of terms) {
        if (title.includes(t)) score += 20;
        if (title.startsWith(t)) score += 10;
        score += Math.min(text.split(t).length - 1, 10);
      }
      results.push({ url: page.u, title: page.t, sec: page.s, score, snip: snippet(page.x, terms[0]) });
      for (const [id, h] of page.h) {
        const hh = h.toLowerCase();
        if (terms.every((t) => hh.includes(t))) {
          results.push({ url: `${page.u}#${id}`, title: h, sec: page.t, score: score + 15 + (hh === q ? 20 : 0), snip: "" });
        }
      }
    }
    results.sort((a, b) => b.score - a.score);
    sel = 0;
    list.innerHTML = results.slice(0, 12).map((r, i) =>
      `<li><a href="${esc(r.url)}"${i === 0 ? ' class="sel"' : ""}><strong>${hl(r.title, terms)}<span class="sr-sec">${esc(r.sec)}</span></strong>${r.snip ? `<span class="sr-snip">${hl(r.snip, terms)}</span>` : ""}</a></li>`
    ).join("") || `<li><a href="${base}/docs/"><strong>No results for “${esc(q)}”</strong><span class="sr-snip">Browse all docs</span></a></li>`;
  };

  const open = async () => {
    lastFocus = document.activeElement;
    box.hidden = false;
    input.focus();
    input.select();
    if (!index) {
      try { index = await (await fetch(`${base}/search-index.json`)).json(); } catch { index = []; }
      run();
    }
  };
  const close = () => { box.hidden = true; lastFocus?.focus?.(); };

  $(".search-open")?.addEventListener("click", open);
  box.addEventListener("click", (e) => { if (e.target.closest("[data-close]")) close(); });
  input.addEventListener("input", run);
  input.addEventListener("keydown", (e) => {
    const links = $$("a", list);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!links.length) return;
      links[sel]?.classList.remove("sel");
      sel = (sel + (e.key === "ArrowDown" ? 1 : -1) + links.length) % links.length;
      links[sel].classList.add("sel");
      links[sel].scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter" && links[sel]) {
      e.preventDefault();
      location.href = links[sel].href;
      close();
    }
  });
  document.addEventListener("keydown", (e) => {
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName) || document.activeElement?.isContentEditable;
    if (e.key === "Escape" && !box.hidden) close();
    else if (!typing && box.hidden && (e.key === "/" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k"))) {
      e.preventDefault();
      open();
    }
  });
})();
