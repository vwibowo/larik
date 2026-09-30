// Builds a compact outline of the page for the model. Interactive elements
// get a data-larik-ref (kept across snapshots of the same document) that the
// click, type and select tools look up with __larikFind. Called with the ref
// of an element to outline only that element's subtree, or "" for the page.
// Returns {url, title, text, refs}, or {error} when the ref isn't found.
((rootRef) => {
  const MAX_TEXT = 160;
  // A frame's document, when this page may read it (same origin).
  const frameDoc = (frame) => {
    try {
      return frame.contentDocument || null;
    } catch (e) {
      return null;
    }
  };
  if (!window.__larik) {
    window.__larik = { next: 1 };
    // Refs can live inside open shadow roots and same-origin iframes, which
    // querySelector on the document misses.
    window.__larikFind = (ref) => {
      const sel = '[data-larik-ref="' + ref + '"]';
      const walk = (root) => {
        const hit = root.querySelector(sel);
        if (hit) return hit;
        for (const el of root.querySelectorAll('*')) {
          if (el.shadowRoot) {
            const h = walk(el.shadowRoot);
            if (h) return h;
          }
          if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') {
            const doc = frameDoc(el);
            const h = doc && walk(doc);
            if (h) return h;
          }
        }
        return null;
      };
      return walk(document);
    };
    // Where an element's frame sits in the top window's viewport: what to add
    // to its getBoundingClientRect to get coordinates the mouse can use.
    window.__larikOffset = (el) => {
      let x = 0, y = 0;
      let win = el.ownerDocument.defaultView;
      while (win && win !== window && win.frameElement) {
        const f = win.frameElement;
        const r = f.getBoundingClientRect();
        x += r.left + f.clientLeft;
        y += r.top + f.clientTop;
        win = win.parent;
      }
      return { x, y };
    };
  }
  const state = window.__larik;

  const clip = (s, n = MAX_TEXT) => {
    s = (s || '').replace(/\s+/g, ' ').trim();
    return s.length > n ? s.slice(0, n - 1) + '…' : s;
  };
  const q = (s) => JSON.stringify(s);

  const hidden = (el) => {
    if (el.hidden || el.getAttribute('aria-hidden') === 'true') return true;
    const cs = el.ownerDocument.defaultView.getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.visibility === 'collapse') return true;
    if (cs.display === 'contents') return false;
    const r = el.getBoundingClientRect();
    return r.width === 0 && r.height === 0 && cs.overflow !== 'visible';
  };

  const SKIP = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'SVG', 'svg', 'HEAD', 'META', 'LINK']);
  const LANDMARK = { NAV: 'navigation', MAIN: 'main', HEADER: 'banner', FOOTER: 'contentinfo', ASIDE: 'complementary', FORM: 'form', DIALOG: 'dialog', UL: 'list', OL: 'list', TABLE: 'table', TR: 'row' };
  const INPUT_ROLE = { checkbox: 'checkbox', radio: 'radio', submit: 'button', button: 'button', reset: 'button', image: 'button', range: 'slider', search: 'searchbox', file: 'filepicker' };

  const labelOf = (el) => {
    const aria = el.getAttribute('aria-label');
    if (aria) return aria;
    const by = el.getAttribute('aria-labelledby');
    if (by) {
      const t = by.split(/\s+/).map((id) => el.ownerDocument.getElementById(id)?.innerText || '').join(' ');
      if (t.trim()) return t;
    }
    if (el.labels && el.labels.length) return el.labels[0].innerText;
    return el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt') || el.getAttribute('name') || '';
  };

  // role of an interactive element, or '' when it isn't one.
  const interactive = (el) => {
    const role = el.getAttribute('role');
    switch (el.tagName) {
      case 'A': return el.hasAttribute('href') ? 'link' : '';
      case 'BUTTON': return 'button';
      case 'SELECT': return 'combobox';
      case 'TEXTAREA': return 'textbox';
      case 'SUMMARY': return 'button';
      case 'INPUT':
        if (el.type === 'hidden') return '';
        return INPUT_ROLE[el.type] || 'textbox';
    }
    if (role && /^(button|link|checkbox|radio|tab|menuitem|option|switch|textbox|combobox|searchbox|slider)$/.test(role)) return role;
    if (el.isContentEditable && !el.parentElement?.isContentEditable) return 'textbox';
    if (el.hasAttribute('onclick') || (el.tabIndex >= 0 && el.hasAttribute('tabindex'))) return role || 'clickable';
    return '';
  };

  const lines = [];
  const refs = [];
  const emit = (depth, s) => lines.push('  '.repeat(depth) + '- ' + s);

  // refOf gives el a ref, or returns the one it has.
  const refOf = (el) => {
    let ref = el.getAttribute('data-larik-ref');
    if (!ref) {
      ref = 'e' + state.next++;
      el.setAttribute('data-larik-ref', ref);
    }
    refs.push(ref);
    return ref;
  };

  const describe = (el, role) => {
    const ref = refOf(el);
    const name = clip(role === 'textbox' || role === 'searchbox' || role === 'combobox' ? labelOf(el) : (labelOf(el) && el.getAttribute('aria-label')) || el.innerText || el.value || labelOf(el), 100);
    let s = role + (name ? ' ' + q(name) : '');
    if (el.tagName === 'INPUT' && !INPUT_ROLE[el.type] && el.type !== 'text') s += ' type=' + el.type;
    if (role === 'textbox' || role === 'searchbox') {
      const v = el.isContentEditable ? el.innerText : el.value;
      if (v && el.type !== 'password') s += ' value=' + q(clip(v, 80));
    }
    if (el.tagName === 'SELECT') {
      const opts = [...el.options].slice(0, 20).map((o) => (o.selected ? '*' : '') + clip(o.text, 40));
      s += ' options=' + q(opts.join(' | ') + (el.options.length > 20 ? ' | …' : ''));
    }
    if (el.checked || el.getAttribute('aria-checked') === 'true') s += ' checked';
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') s += ' disabled';
    if (el.getAttribute('aria-expanded')) s += ' expanded=' + el.getAttribute('aria-expanded');
    if (role === 'link') {
      const href = el.getAttribute('href');
      if (href && !href.startsWith('javascript:')) s += ' -> ' + clip(el.href, 120);
    }
    return s + ' [ref=' + ref + ']';
  };

  let text = [];
  const flushText = (depth) => {
    const t = clip(text.join(' '), 300);
    text = [];
    if (t) emit(depth, 'text ' + q(t));
  };

  // visit outlines one node at depth; walk does a node's children.
  const visit = (child, depth) => {
    if (child.nodeType === Node.TEXT_NODE) {
      if (child.data.trim()) text.push(child.data);
      return;
    }
    if (child.nodeType !== Node.ELEMENT_NODE || SKIP.has(child.tagName)) return;
    const el = child;
    if (hidden(el)) return;
    const role = interactive(el);
    if (role) {
      flushText(depth);
      emit(depth, describe(el, role));
      return;
    }
    if (/^H[1-6]$/.test(el.tagName)) {
      flushText(depth);
      emit(depth, 'heading[' + el.tagName[1] + '] ' + q(clip(el.innerText)));
      return;
    }
    if (el.tagName === 'IMG') {
      const alt = el.getAttribute('alt');
      if (alt) text.push('[image: ' + alt + ']');
      return;
    }
    if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') {
      flushText(depth);
      const label = q(clip(el.getAttribute('title') || el.getAttribute('name') || el.src, 100));
      const doc = frameDoc(el);
      if (doc && doc.body) {
        emit(depth, 'iframe ' + label + ' [ref=' + refOf(el) + ']:');
        walk(doc.body, depth + 1);
        flushText(depth + 1);
      } else {
        // Another origin: the browser won't let this page read it.
        emit(depth, 'iframe ' + label + ' -> ' + clip(el.src, 120) + ' (another site: contents not shown; open that URL with browser_navigate to read it)');
      }
      return;
    }
    const landmark = LANDMARK[el.tagName] || el.getAttribute('role');
    const block = landmark || /^(DIV|P|LI|SECTION|ARTICLE|TD|TH|DD|DT|PRE|BLOCKQUOTE|LABEL|FIGCAPTION)$/.test(el.tagName);
    if (block) flushText(depth);
    if (landmark && LANDMARK[el.tagName] !== 'row') {
      // Containers have refs too, to snapshot or screenshot just that part.
      emit(depth, landmark + ' [ref=' + refOf(el) + ']:');
      walk(el, depth + 1);
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      flushText(depth + 1);
    } else {
      walk(el, depth);
      if (el.shadowRoot) walk(el.shadowRoot, depth);
      if (block) flushText(depth);
    }
  };
  const walk = (node, depth) => {
    for (const child of node.childNodes) visit(child, depth);
  };

  if (rootRef) {
    const root = window.__larikFind(rootRef);
    if (!root) return { error: 'noref' };
    visit(root, 0);
  } else if (document.body) {
    walk(document.body, 0);
  }
  flushText(0);
  return { url: location.href, title: document.title, text: lines.join('\n'), refs: refs.length };
})
