 
(function () {
  "use strict";

  var host = document.getElementById("rb");
  var prose = host ? host.closest(".prose") : null;
  if (!prose) { return; }
  var data = null;
  try { data = JSON.parse(host.getAttribute("data-board")); } catch (e) { data = null; }
  if (!data || !Array.isArray(data.stages)) { return; }

   
  var STATUS = {
    ships: { chip: "allow", mark: '<circle cx="6" cy="6" r="5.5" fill="var(--ok)"/><path d="m3.5 6.2 1.7 1.7 3.3-3.6" fill="none" stroke="var(--bg)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>' },
    designing: { chip: "next", mark: '<circle cx="6" cy="6" r="4.4" fill="none" stroke="var(--accent)" stroke-width="1.6"/><path d="M6 1.6a4.4 4.4 0 0 1 0 8.8Z" fill="var(--accent)"/>' },
    designed: { chip: "next", mark: '<circle cx="6" cy="6" r="5" fill="var(--accent)"/>' },
    planned: { chip: "idea", mark: '<circle cx="6" cy="6" r="4.4" fill="none" stroke="var(--border-strong)" stroke-width="1.6"/>' }
  };
   
  var ICONS = {
    identity: '<circle cx="8" cy="5.6" r="2.6"/><path d="M2.8 13.6c.7-2.5 2.7-3.8 5.2-3.8s4.5 1.3 5.2 3.8"/>',
    approvals: '<rect x="4.4" y="1.5" width="7.2" height="13" rx="1.7"/><path d="m6.3 8.3 1.3 1.3 2.2-2.5"/>',
    gateway: '<path d="M1.8 8h3.4M10.8 8h3.4"/><rect x="5.2" y="4.4" width="5.6" height="7.2" rx="1.3"/><path d="M8 6.7v2.6"/>',
    analytics: '<path d="M3 13.2V8.6M8 13.2V3M13 13.2V6.4"/>',
    clients: '<path d="M2.5 5 8 2l5.5 3v6L8 14l-5.5-3V5Z"/><path d="M2.5 5 8 8l5.5-3M8 8v6"/>',
    fleet: '<circle cx="8" cy="3.5" r="1.8"/><circle cx="3.5" cy="11.8" r="1.8"/><circle cx="12.5" cy="11.8" r="1.8"/><path d="M7.1 5.1 4.4 10.2M8.9 5.1l2.7 5.1M5.3 11.8h5.4"/>'
  };
  var PLAIN_ICON = '<rect x="2.5" y="2.5" width="11" height="11" rx="2.6"/><circle cx="8" cy="8" r="1.4"/>';

  function icon(lane) { return '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (ICONS[lane.id] || PLAIN_ICON) + "</svg>"; }
  function mark(s) { return '<svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">' + STATUS[s].mark + "</svg>"; }
  function el(tag, cls, html) { var e = document.createElement(tag); if (cls) { e.className = cls; } if (html != null) { e.innerHTML = html; } return e; }
  function esc(s) { return String(s).split("&").join("&amp;").split("<").join("&lt;").split('"').join("&quot;"); }
  function list(v) { return v == null ? [] : [].concat(v); }
   
  function key(s) { return String(s).replace(/[‘’]/g, "'").replace(/\s+/g, " ").trim(); }
  function calm() { return matchMedia("(prefers-reduced-motion: reduce)").matches; }

  var stages = data.stages.map(function (s) {
    return { id: s.id, label: String(s.label || s.heading), short: String(s.short || s.label || s.heading), note: String(s.note || ""), count: 0, h: document.getElementById(s.id) };
  }).filter(function (st) { return st.h; });
  if (!stages.length) { return; }
  var lanes = list(data.lanes).map(function (l) { return { id: String(l.id), label: String(l.label), names: list(l.items) }; });
  var laneOf = {}, other = null;
  lanes.forEach(function (l) { l.names.forEach(function (n) { laneOf[key(n)] = l; }); });
  function laneFor(name) {
    if (laneOf[key(name)]) { return laneOf[key(name)]; }
    if (!other) { other = { id: "other", label: "Other", names: [] }; lanes.push(other); }
    return other;
  }

   
  var items = [], byName = {};
  stages.forEach(function (st, si) {
    var n = st.h.nextElementSibling;
    while (n && n.tagName !== "H2" && !n.querySelector("table")) { n = n.nextElementSibling; }
    if (!n || n.tagName === "H2") { return; }
    n.querySelectorAll("tbody tr").forEach(function (tr) {
      var td = tr.children, name = td[0].textContent.trim();
      var status = td[2] ? td[2].textContent.trim() : "ships";
      if (!STATUS[status]) { status = "planned"; }
      var it = { name: name, what: td[1].innerHTML, status: status, stage: st, si: si, lane: laneFor(name), cell: td[0], bundle: null };
      items.push(it); byName[key(name)] = it; st.count++;
    });
  });
  lanes.forEach(function (l, li) { l.i = li; });
  items.forEach(function (it) {
    var g = el("span", "rb-g", icon(it.lane));
    g.title = it.lane.label;
    it.cell.insertBefore(g, it.cell.firstChild);
  });
  var needs = {};
  list(data.needs).forEach(function (n) { needs[key(n.item)] = list(n.waits_on).map(key); });
  items.forEach(function (it) {
    it.needs = (needs[key(it.name)] || []).map(function (n) { return byName[n]; }).filter(Boolean);
  });
  items.forEach(function (it) {
    it.unlocks = items.filter(function (o) { return o.needs.indexOf(it) >= 0; });
  });
  var bundles = list(data.bundles).map(function (b, bi) {
    var members = list(b.items).map(function (n) { return byName[key(n)]; }).filter(Boolean);
    var label = String(b.label || "");
    members.forEach(function (it) { it.bundle = { i: bi, label: label, others: members.filter(function (o) { return o !== it; }) }; });
    return { label: label, members: members };
  }).filter(function (b) { return b.members.length; });
  function statusLabel(it) { return it.status === "ships" ? "ships in " + it.stage.label : it.status; }
   
  var todayAt = -1;
  stages.some(function (st, si) {
    if (items.some(function (it) { return it.stage === st && it.status !== "ships"; })) { todayAt = si; }
    return todayAt >= 0;
  });

   
  var legend = Object.keys(STATUS).map(function (s) {
    var first = items.filter(function (it) { return it.status === s; })[0];
    return first ? "<li>" + mark(s) + esc(statusLabel(first)) + "</li>" : "";
  }).join("") + bundles.map(function (b) { return '<li><span class="rb-dash"></span>' + esc(b.label) + "</li>"; }).join("");
  var tools = el("div", "rb-tools", '<ul class="rb-legend">' + legend + "</ul>" +
    '<button type="button" class="rb-replay"><svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M13.5 8a5.5 5.5 0 1 1-1.7-4"/><path d="M13.6 2.2v3.1h-3.1"/></svg>Replay</button>');
  var board = el("div", "rb-board"), grid = el("div", "rb-grid");
  board.setAttribute("role", "group");
  board.setAttribute("aria-label", "Roadmap board: lanes by area, columns by time");
  grid.style.setProperty("--rb-cols", stages.length);
  grid.appendChild(el("div", "rb-corner"));
  function edge(si) { return (si === 0 ? " s-first" : "") + (si === stages.length - 1 ? " s-last" : ""); }
  stages.forEach(function (st, si) {
    var h = el("div", "rb-h" + edge(si), "<b>" + esc(st.label) + '</b><span class="rb-n">' + st.count + '</span><span class="rb-sub">' + esc(st.note) + "</span>");
    h.style.setProperty("--o", si * 100);
    grid.appendChild(h); st.head = h;
  });
  var cells = {};
  lanes.forEach(function (l, li) {
    var lb = el("button", "rb-lane" + (li === 0 ? " r0" : ""), icon(l) + "<span>" + esc(l.label) + "</span>");
    lb.type = "button"; lb.setAttribute("aria-pressed", "false");
    grid.appendChild(lb); l.btn = lb;
    stages.forEach(function (_, si) {
      var c = el("div", "rb-cell" + edge(si) + (li === 0 ? " r0" : "") + " is-empty", '<span class="rb-cl">' + esc(l.label) + "</span>");
      c.style.setProperty("--o", si * 100 + li + 1);
      grid.appendChild(c); cells[li + "/" + si] = c;
    });
  });
  items.forEach(function (it) {
    var c = cells[it.lane.i + "/" + it.si];
    c.classList.remove("is-empty");
    var b = el("button", "rb-item", mark(it.status) + '<span class="rb-name">' + esc(it.name) + "</span>");
    b.type = "button"; b.dataset.status = it.status; b.setAttribute("aria-expanded", "false");
    b.setAttribute("aria-label", it.name + ", " + statusLabel(it) + ", " + it.stage.label);
    b.style.setProperty("--s", it.si); b.style.setProperty("--l", it.lane.i);
    it.btn = b;
    if (it.bundle) {
      var bw = c.querySelector('.rb-bundle[data-b="' + it.bundle.i + '"]');
      if (!bw) { bw = el("div", "rb-bundle", "<span>" + esc(it.bundle.label) + "</span>"); bw.dataset.b = it.bundle.i; c.appendChild(bw); }
      bw.appendChild(b);
    } else { c.appendChild(b); }
  });
  board.appendChild(grid);
  var links = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  links.setAttribute("class", "rb-links"); links.setAttribute("aria-hidden", "true");
  board.appendChild(links);
  var today = todayAt >= 0 ? el("div", "rb-today", "<span>today</span>") : null;
  if (today) { board.appendChild(today); }
  board.appendChild(el("div", "rb-sweep"));
  var sheet = el("div", "rb-sheet", '<div class="rb-sheet-in"><div class="rb-sheet-body" role="region" aria-live="polite" aria-label="The selected roadmap item"></div></div>');
  host.appendChild(tools); host.appendChild(board); host.appendChild(sheet);

   
  function layout() {
    var br = board.getBoundingClientRect();
    board.style.setProperty("--rb-w", (br.width - grid.firstElementChild.getBoundingClientRect().width + 90) + "px");
    if (today) { today.style.left = (stages[todayAt].head.getBoundingClientRect().left - br.left - 1) + "px"; }
    var out = '<defs><marker id="rb-ah" viewBox="0 0 10 10" refX="8.5" refY="5" markerWidth="5.5" markerHeight="5.5" orient="auto"><path d="M0 0 10 5 0 10z"/></marker></defs>';
    items.forEach(function (it) {
      it.needs.forEach(function (src) {
        var a = src.btn.getBoundingClientRect(), b = it.btn.getBoundingClientRect(), p;
        if (b.left > a.right + 4) {
          var x1 = a.right - br.left, y1 = a.top + a.height / 2 - br.top, x2 = b.left - br.left - 2, y2 = b.top + b.height / 2 - br.top, mx = (x1 + x2) / 2;
          p = "M" + x1 + " " + y1 + " C" + mx + " " + y1 + " " + mx + " " + y2 + " " + x2 + " " + y2;
        } else {
          var x = a.left - br.left, ya = a.top + a.height / 2 - br.top, yb = b.top + b.height / 2 - br.top;
          p = "M" + x + " " + ya + " C" + (x - 13) + " " + ya + " " + (x - 13) + " " + yb + " " + (x - 2) + " " + yb;
        }
        out += '<path pathLength="1" marker-end="url(#rb-ah)" data-a="' + items.indexOf(src) + '" data-b="' + items.indexOf(it) + '" d="' + p + '"/>';
      });
    });
    links.innerHTML = out;
  }

   
  var laneOn = null;
  function lit(set) {
    board.classList.toggle("rb-focus", !!set);
    items.forEach(function (it) { it.btn.classList.toggle("is-on", !!set && set.indexOf(it) >= 0); });
    lanes.forEach(function (l) { l.btn.classList.toggle("is-on", !!set && set.some(function (it) { return it.lane === l; })); });
    links.querySelectorAll("path[data-a]").forEach(function (p) {
      p.classList.toggle("is-hot", !!set && set.indexOf(items[+p.dataset.a]) >= 0 && set.indexOf(items[+p.dataset.b]) >= 0);
    });
  }
  function rest() { lit(laneOn ? items.filter(function (it) { return it.lane === laneOn; }) : null); }
  function related(it) { return [it].concat(it.needs, it.unlocks, it.bundle ? it.bundle.others : []); }
  items.forEach(function (it) {
    var rel = related(it);
    function on() { if (rel.length > 1) { lit(rel); } }
    it.btn.addEventListener("mouseenter", on); it.btn.addEventListener("focus", on);
    it.btn.addEventListener("mouseleave", rest); it.btn.addEventListener("blur", rest);
    it.btn.addEventListener("click", function () { select(current === it ? null : it); });
  });
  lanes.forEach(function (l) {
    l.btn.addEventListener("click", function () {
      laneOn = laneOn === l ? null : l;
      lanes.forEach(function (o) { o.btn.setAttribute("aria-pressed", String(o === laneOn)); });
      rest();
    });
  });

   
  var current = null, body = sheet.querySelector(".rb-sheet-body");
  function names(group) { return group.map(function (o) { return '<button type="button" class="rb-rel" data-i="' + items.indexOf(o) + '">' + esc(o.name) + "</button>"; }).join(" "); }
  function render(it) {
    var rels = "";
    if (it.needs.length) { rels += "<span>Waits on</span> " + names(it.needs); }
    if (it.unlocks.length) { rels += "<span>Opens the way for</span> " + names(it.unlocks); }
    if (it.bundle && it.bundle.others.length) { rels += "<span>Ships in " + esc(it.bundle.label) + " with</span> " + names(it.bundle.others); }
    body.innerHTML = '<div class="rb-sheet-head"><h3>' + esc(it.name) + '</h3><span class="chip ' + STATUS[it.status].chip + '">' + esc(statusLabel(it)) +
      '</span><span class="rb-meta">' + esc(it.stage.label) + " · " + esc(it.lane.label) + "</span></div><p>" + it.what + "</p>" +
      (rels ? '<div class="rb-rels">' + rels + "</div>" : "") +
      '<button type="button" class="rb-close" aria-label="Close"><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><path d="m3.5 3.5 9 9M12.5 3.5l-9 9"/></svg></button>';
    body.querySelector(".rb-close").addEventListener("click", function () { var b = current && current.btn; select(null); if (b) { b.focus(); } });
    body.querySelectorAll(".rb-rel").forEach(function (r) { r.addEventListener("click", function () { select(items[+r.dataset.i]); }); });
  }
  function apply(it) {
    items.forEach(function (o) { o.btn.setAttribute("aria-expanded", String(o === it)); });
    current = it;
    if (it) { render(it); sheet.classList.add("open"); } else { sheet.classList.remove("open"); }
  }
  function select(it) {
    if (!it || calm() || !document.startViewTransition) { apply(it); if (it) { reveal(); } return; }
    var from = it.btn.querySelector(".rb-name");
    from.style.viewTransitionName = "rb-title";
    var t = document.startViewTransition(function () {
      from.style.viewTransitionName = "";
      apply(it);
      body.querySelector("h3").style.viewTransitionName = "rb-title";
    });
    t.finished.then(function () { var h = body.querySelector("h3"); if (h) { h.style.viewTransitionName = ""; } reveal(); });
  }
  function reveal() {
    if (sheet.getBoundingClientRect().bottom > innerHeight - 8) { sheet.scrollIntoView({ block: "nearest", behavior: calm() ? "auto" : "smooth" }); }
  }
  document.addEventListener("keydown", function (e) { if (e.key === "Escape" && current) { var b = current.btn; select(null); b.focus(); } });

   
  var mini = el("nav", "rb-mini");
  mini.setAttribute("aria-label", "Roadmap stages");
  mini.style.setProperty("--rb-cols", stages.length);
  stages.forEach(function (st) {
    var a = el("a", "", "<b>" + esc(st.short) + "</b><span>" + st.count + "</span>");
    a.href = "#" + st.id; mini.appendChild(a); st.link = a;
  });
  var top = host;
  while (top.parentNode !== prose) { top = top.parentNode; }
  prose.insertBefore(mini, top.nextSibling);
  var end = stages[stages.length - 1].h.nextElementSibling;
  while (end && end.tagName !== "H2") { end = end.nextElementSibling; }
  var tick = false;
  function progress() {
    tick = false;
    var ref = scrollY + mini.getBoundingClientRect().bottom + 24;
    var tops = stages.map(function (st) { return st.h.getBoundingClientRect().top + scrollY; });
    tops.push(end ? end.getBoundingClientRect().top + scrollY : prose.getBoundingClientRect().bottom + scrollY);
    var active = -1;
    stages.forEach(function (st, i) {
      var p = Math.max(0, Math.min(1, (ref - tops[i]) / (tops[i + 1] - tops[i])));
      st.link.style.setProperty("--p", p.toFixed(3));
      if (p > 0 && p < 1) { active = i; }
    });
    stages.forEach(function (st, i) { st.link.setAttribute("aria-current", String(i === active)); });
  }
  addEventListener("scroll", function () { if (!tick) { tick = true; requestAnimationFrame(progress); } }, { passive: true });

   
  function play() {
    if (calm()) { return; }
    board.classList.remove("rb-play"); void board.offsetWidth; board.classList.add("rb-play");
    setTimeout(function () { board.classList.remove("rb-play"); }, 2600);
  }
  tools.querySelector(".rb-replay").addEventListener("click", play);
  layout(); progress();
  if ("ResizeObserver" in window) { new ResizeObserver(layout).observe(board); } else { addEventListener("resize", layout); }
  if (document.fonts && document.fonts.ready) { document.fonts.ready.then(layout); }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (es) { if (es[0].isIntersecting) { io.disconnect(); play(); } }, { threshold: .35 });
    io.observe(board);
  }
})();
