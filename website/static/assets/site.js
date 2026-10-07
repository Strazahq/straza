 
(function () {
  "use strict";

  var root = document.documentElement;
  var THEME_KEY = "straza-theme";
  var MODE_KEY = "straza-mode";

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) {   }
  }
  function stored(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }

   
  function headerOffset() {
    var h = document.querySelector(".dhead");
    if (!h || getComputedStyle(h).position !== "sticky") { return 0; }
    return h.getBoundingClientRect().height;
  }

   
  function initScrollPadding() {
    function set() { root.style.scrollPaddingTop = (headerOffset() + 16) + "px"; }
    set();
    window.addEventListener("resize", set);
  }

   

  function systemTheme() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }
  function effectiveTheme() {
    return root.getAttribute("data-theme") || systemTheme();
  }
  function updateToggle() {
    var btn = document.getElementById("theme-toggle");
    if (!btn) { return; }
    var next = effectiveTheme() === "dark" ? "light" : "dark";
    btn.setAttribute("aria-label", "Switch to the " + next + " theme");
    btn.title = "Switch to the " + next + " theme";
  }
   
  function syncDiagramFrames() {
    var theme = effectiveTheme();
    var frames = document.querySelectorAll("figure.diagram iframe");
    for (var i = 0; i < frames.length; i++) {
      var src = frames[i].getAttribute("src");
      if (!src) { continue; }
      var next = src.split("#")[0].split("?")[0] + "?theme=" + theme;
      if (src !== next) { frames[i].setAttribute("src", next); }
    }
  }
  function initTheme() {
    syncDiagramFrames();
    updateToggle();
    var btn = document.getElementById("theme-toggle");
    if (btn) {
      btn.addEventListener("click", function () {
        var theme = effectiveTheme() === "dark" ? "light" : "dark";
        root.setAttribute("data-theme", theme);
        store(THEME_KEY, theme);
        syncDiagramFrames();
        updateToggle();
      });
    }
    if (window.matchMedia) {
      var mq = window.matchMedia("(prefers-color-scheme: light)");
      if (mq.addEventListener) {
        mq.addEventListener("change", function () {
          if (!root.getAttribute("data-theme")) { syncDiagramFrames(); updateToggle(); }
        });
      }
    }
  }

   

  function initMode() {
    var sw = document.querySelector(".modesw");
    if (!sw) { return; }
    var buttons = sw.querySelectorAll("button[data-mode]");
    function sync() {
      var mode = root.getAttribute("data-mode");
      for (var i = 0; i < buttons.length; i++) {
        buttons[i].setAttribute("aria-pressed", String(buttons[i].getAttribute("data-mode") === mode));
      }
    }
    for (var i = 0; i < buttons.length; i++) {
      buttons[i].addEventListener("click", function () {
        var mode = this.getAttribute("data-mode");
        root.setAttribute("data-mode", mode);
        store(MODE_KEY, mode);
        sync();
      });
    }
    sync();
    sw.hidden = false;
  }

   

  function fallbackCopy(text) {
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
    document.body.removeChild(ta);
    return ok;
  }
  function initCopyButtons() {
    var blocks = document.querySelectorAll("pre > code");
    for (var i = 0; i < blocks.length; i++) {
      (function (code) {
        var btn = document.createElement("button");
        btn.type = "button";
        btn.className = "copy-btn";
        btn.textContent = "Copy";
        btn.setAttribute("aria-label", "Copy the code");
        btn.addEventListener("click", function () {
          var text = code.innerText.replace(/\n$/, "");
          var done = function (ok) {
            btn.textContent = ok ? "Copied" : "Copy failed";
            btn.classList.toggle("is-copied", ok);
            setTimeout(function () { btn.textContent = "Copy"; btn.classList.remove("is-copied"); }, 1600);
          };
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(fallbackCopy(text)); });
          } else {
            done(fallbackCopy(text));
          }
        });
        var bar = code.closest(".cmd") ? code.closest(".cmd").querySelector(".cmd-bar") : null;
        if (bar && !bar.querySelector(".copy-btn")) {
          var who = bar.querySelector(".who");
          btn.setAttribute("aria-label", "Copy the command" + (who ? " for " + who.textContent : ""));
          bar.appendChild(btn);
        } else {
          code.parentElement.appendChild(btn);
        }
      })(blocks[i]);
    }
  }

   

   
  function pageHeadings() {
    var all = document.querySelectorAll(".main h2[id], .main h3[id]");
    var out = [];
    for (var i = 0; i < all.length; i++) {
      if (!all[i].closest(".m-console, .m-cli")) { out.push(all[i]); }
    }
    return out;
  }
  function initTOC() {
    var list = document.getElementById("toc-list");
    if (!list) { return; }
    var heads = pageHeadings();
    if (heads.length === 0) {
      var body = document.querySelector(".dbody");
      if (body) { body.classList.add("norail"); }
      list.closest(".rail").hidden = true;
      return;
    }
    var links = [];
    for (var i = 0; i < heads.length; i++) {
      var a = document.createElement("a");
      a.href = "#" + heads[i].id;
      a.textContent = heads[i].textContent.trim();
      if (heads[i].tagName === "H3") { a.className = "toc-child"; }
      list.appendChild(a);
      links.push(a);
    }
    var ticking = false;
    function setActive() {
      ticking = false;
       
      var line = headerOffset() + 24;
      var current = heads[0];
      for (var j = 0; j < heads.length; j++) {
        if (heads[j].getBoundingClientRect().top <= line) { current = heads[j]; }
      }
      for (var k = 0; k < links.length; k++) {
        var on = links[k].hash === "#" + current.id;
        links[k].classList.toggle("is-active", on);
        if (on) { links[k].setAttribute("aria-current", "location"); } else { links[k].removeAttribute("aria-current"); }
      }
    }
    window.addEventListener("scroll", function () {
      if (!ticking) { ticking = true; window.requestAnimationFrame(setActive); }
    }, { passive: true });
    setActive();
  }
   
  function initHeadingAnchors() {
    var heads = document.querySelectorAll(".main h2[id], .main h3[id]");
    for (var i = 0; i < heads.length; i++) {
      var h = heads[i];
      if (h.querySelector(".h-anchor") || h.closest(".search-dialog")) { continue; }
      var a = document.createElement("a");
      a.className = "h-anchor";
      a.href = "#" + h.id;
      a.textContent = "#";
      a.setAttribute("aria-label", "Link to this section: " + h.textContent.trim());
      h.appendChild(a);
    }
  }

   

  function initNav() {
    var nav = document.getElementById("lnav");
    var btn = document.querySelector(".menu-btn");
    if (!nav) { return; }
    var current = nav.querySelector('li a[aria-current="page"]');
    if (current && nav.scrollHeight > nav.clientHeight) {
      nav.scrollTop = Math.max(0, current.offsetTop - nav.clientHeight / 3);
    }
    if (!btn) { return; }
    btn.hidden = false;
    function setOpen(open) {
      if (open) {
        var head = document.querySelector(".dhead");
        root.style.setProperty("--menu-top", head.getBoundingClientRect().bottom + "px");
      }
      root.classList.toggle("nav-open", open);
      btn.setAttribute("aria-expanded", String(open));
      if (open && current) { current.scrollIntoView({ block: "center" }); }
    }
    btn.addEventListener("click", function () { setOpen(!root.classList.contains("nav-open")); });
    nav.addEventListener("click", function (e) { if (e.target.closest("a")) { setOpen(false); } });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && root.classList.contains("nav-open")) { setOpen(false); btn.focus(); }
    });
    if (window.matchMedia) {
      var wide = window.matchMedia("(min-width: 881px)");
      if (wide.addEventListener) { wide.addEventListener("change", function () { if (wide.matches) { setOpen(false); } }); }
    }
  }

   

  function initSmoothScroll() {
    document.addEventListener("click", function (e) {
      var a = e.target && e.target.closest ? e.target.closest('a[href^="#"]') : null;
      if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) { return; }
      var id = decodeURIComponent(a.getAttribute("href").slice(1));
      var el = id ? document.getElementById(id) : null;
      if (!el) { return; }
      e.preventDefault();
      var smooth = !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      el.scrollIntoView({ behavior: smooth ? "smooth" : "auto", block: "start" });
      if (!el.hasAttribute("tabindex")) { el.setAttribute("tabindex", "-1"); }
      el.focus({ preventScroll: true });
      history.pushState(null, "", "#" + id);
    });
  }

   

  function initDiagrams() {
    var svgs = document.querySelectorAll(".fig svg[data-say]");
    for (var i = 0; i < svgs.length; i++) {
      (function (svg) {
        var say;
        try { say = JSON.parse(svg.getAttribute("data-say")); } catch (e) { return; }
        var groups = svg.querySelectorAll("[data-s]");
        if (!Array.isArray(say) || say.length < 2 || groups.length === 0) { return; }
        var fig = svg.closest(".fig");
        var canvas = svg.closest(".canvas");
        var ctl = document.createElement("div");
        ctl.className = "ctl";
        ctl.setAttribute("role", "group");
        ctl.setAttribute("aria-label", "Walk through the diagram");
        var line = document.createElement("p");
        line.className = "say";
        line.setAttribute("aria-live", "polite");
        var buttons = [];
        function show(n) {
          for (var b = 0; b < buttons.length; b++) { buttons[b].setAttribute("aria-pressed", String(b === n)); }
          svg.classList.toggle("stepping", n > 0);
          for (var g = 0; g < groups.length; g++) {
            groups[g].classList.toggle("lit", parseInt(groups[g].getAttribute("data-s"), 10) === n);
          }
          line.textContent = say[n];
        }
        for (var n = 0; n < say.length; n++) {
          var b = document.createElement("button");
          b.type = "button";
          b.textContent = n === 0 ? "Whole picture" : "Step " + n;
          b.addEventListener("click", show.bind(null, n));
          buttons.push(b);
          ctl.appendChild(b);
        }
        fig.insertBefore(ctl, canvas);
        canvas.insertAdjacentElement("afterend", line);
        show(0);
      })(svgs[i]);
    }
  }

   

  var lightbox = null;
  function openLightbox(link) {
    var base = siteRoot() || "";
    if (!lightbox) {
      lightbox = document.createElement("dialog");
      lightbox.className = "lightbox";
      lightbox.setAttribute("aria-label", "The whole screen");
      var bar = document.createElement("div");
      bar.className = "lightbox-bar";
      var title = document.createElement("span");
      title.textContent = "The whole screen";
      var close = document.createElement("button");
      close.type = "button";
      close.textContent = "Close";
      close.addEventListener("click", function () { lightbox.close(); });
      bar.appendChild(title);
      bar.appendChild(close);
      lightbox.appendChild(bar);
      ["sd", "sl"].forEach(function (cls) {
        var img = document.createElement("img");
        img.className = cls;
        lightbox.appendChild(img);
      });
      lightbox.addEventListener("click", function (e) { if (e.target === lightbox) { lightbox.close(); } });
      document.body.appendChild(lightbox);
    }
    var dark = lightbox.querySelector("img.sd");
    var light = lightbox.querySelector("img.sl");
    dark.src = base + link.getAttribute("data-full-dark");
    light.src = base + link.getAttribute("data-full-light");
    dark.alt = light.alt = link.getAttribute("data-alt") || "";
    lightbox.showModal();
  }
  function initShots() {
    if (typeof HTMLDialogElement !== "function") { return; }
    document.addEventListener("click", function (e) {
      var link = e.target.closest ? e.target.closest("a.zoom[data-full-dark]") : null;
      if (!link || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) { return; }
      e.preventDefault();
      openLightbox(link);
    });
  }

   

  function initCompare() {
    var blocks = document.querySelectorAll(".compare");
    for (var i = 0; i < blocks.length; i++) {
      (function (block) {
        var filter = block.querySelector(".filter");
        var buttons = filter.querySelectorAll("button[data-area]");
        var rows = block.querySelectorAll("tbody tr[data-area]");
        for (var j = 0; j < buttons.length; j++) {
          buttons[j].addEventListener("click", function () {
            var area = this.getAttribute("data-area");
            for (var k = 0; k < buttons.length; k++) { buttons[k].setAttribute("aria-pressed", String(buttons[k] === this)); }
            for (var r = 0; r < rows.length; r++) { rows[r].hidden = area !== "" && rows[r].getAttribute("data-area") !== area; }
          });
        }
        filter.hidden = false;
      })(blocks[i]);
    }
  }

   

  var searchIndex = null;
  var searchState = "idle";  

  function siteRoot() {
    var links = document.querySelectorAll('link[rel="stylesheet"]');
    for (var i = 0; i < links.length; i++) {
      var at = links[i].href.indexOf("assets/site.css");
      if (at !== -1) { return links[i].href.slice(0, at); }
    }
    return null;
  }

  function loadSearchIndex(then) {
    if (searchState !== "idle") { return; }
    var base = siteRoot();
    if (!base || typeof fetch !== "function") { searchState = "failed"; then(); return; }
    searchState = "loading";
    fetch(base + "index.json", { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) { throw new Error(String(r.status)); } return r.json(); })
      .then(function (data) { searchIndex = data; searchState = "ready"; then(); })
      .catch(function () { searchState = "failed"; then(); });
  }

  function initSearch() {
    var input = document.getElementById("site-search");
    var box = document.getElementById("search-results");
    var status = document.getElementById("search-status");
    var dialog = document.getElementById("search-dialog");
    var opener = document.getElementById("search-open");
    if (!input || !box || !status || !dialog || !opener || !dialog.showModal || !window.StrazaDocsSearch) { return; }
    var section = "";
    var previousFocus = null;
    var filters = dialog.querySelectorAll("[data-search-section]");
    var sectionNames = {};
    for (var i = 0; i < filters.length; i++) {
      sectionNames[filters[i].getAttribute("data-search-section")] = filters[i].textContent;
    }
    opener.hidden = false;

    function message(text) {
      box.replaceChildren();
      status.textContent = text;
    }

    function highlight(target, text, query) {
      var lower = text.toLowerCase();
      var tokens = query.toLowerCase().trim().split(/\s+/).filter(Boolean);
      var position = 0;
      while (position < text.length) {
        var at = -1;
        var length = 0;
        for (var i = 0; i < tokens.length; i++) {
          var match = lower.indexOf(tokens[i], position);
          if (match >= 0 && (at < 0 || match < at || (match === at && tokens[i].length > length))) {
            at = match; length = tokens[i].length;
          }
        }
        if (at < 0) { target.appendChild(document.createTextNode(text.slice(position))); break; }
        target.appendChild(document.createTextNode(text.slice(position, at)));
        var mark = document.createElement("mark");
        mark.textContent = text.slice(at, at + length);
        target.appendChild(mark);
        position = at + length;
      }
    }

    function render() {
      var query = input.value.trim();
      if (query.length < 2) { message("Search page titles, commands, and article text. Enter at least two characters."); return; }
      if (searchState === "idle" || searchState === "loading") { message("Loading the search index…"); return; }
      if (searchState !== "ready") {
        message("Search could not load. Close search and use the section links, or reopen search to try again.");
        return;
      }
      var base = siteRoot();
      var result = window.StrazaDocsSearch(searchIndex, query, 10, section);
      box.replaceChildren();
      if (result.hits.length === 0) {
        message("No pages match “" + query + "”" + (section ? " in " + sectionNames[section] : "") + ". Try a shorter phrase or another section.");
        return;
      }
      for (var j = 0; j < result.hits.length; j++) {
        var hit = result.hits[j];
        var entry = hit.entry;
        var a = document.createElement("a");
        a.href = base + entry.p + (entry.a ? "#" + entry.a : "");
        var page = document.createElement("span");
        page.className = "result-page";
        page.textContent = (sectionNames[entry.p.split("/")[0]] || "Documentation") + (entry.a ? " / " + entry.pt : "");
        var title = document.createElement("span");
        title.className = "result-title";
        title.textContent = entry.t;
        a.appendChild(page); a.appendChild(title);
        if (hit.snippet) {
          var excerpt = document.createElement("span");
          excerpt.className = "result-snippet";
          highlight(excerpt, hit.snippet, query);
          a.appendChild(excerpt);
        }
        box.appendChild(a);
      }
      status.textContent = result.total + (result.total === 1 ? " page found" : " pages found") +
        (result.total > result.hits.length ? " · showing the first " + result.hits.length : "");
      box.scrollTop = 0;
    }

    function open() {
      previousFocus = document.activeElement;
      dialog.showModal();
      document.body.classList.add("search-is-open");
      input.focus();
      if (searchState === "failed") { searchState = "idle"; }
      loadSearchIndex(function () { if (dialog.open) { render(); } });
      render();
    }
    opener.addEventListener("click", open);
    document.getElementById("search-close").addEventListener("click", function () { dialog.close(); });
    dialog.addEventListener("close", function () {
      document.body.classList.remove("search-is-open");
      if (previousFocus) { previousFocus.focus(); }
    });
    input.addEventListener("input", render);
    box.addEventListener("click", function (event) {
      var link = event.target.closest("a");
      if (!link || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) { return; }
      if (link.origin === location.origin && link.pathname === location.pathname && link.search === location.search && link.hash) {
        var target = document.getElementById(decodeURIComponent(link.hash.slice(1)));
        if (target) {
          if (!target.hasAttribute("tabindex")) { target.setAttribute("tabindex", "-1"); }
          previousFocus = target;
        }
      }
      dialog.close();
    });
    for (var j = 0; j < filters.length; j++) {
      filters[j].addEventListener("click", function () {
        section = this.getAttribute("data-search-section");
        for (var k = 0; k < filters.length; k++) { filters[k].setAttribute("aria-pressed", String(filters[k] === this)); }
        render();
      });
    }
    dialog.addEventListener("keydown", function (event) {
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") { return; }
      if (document.activeElement !== input && !box.contains(document.activeElement)) { return; }
      var items = Array.from(box.querySelectorAll("a"));
      if (!items.length) { return; }
      event.preventDefault();
      var active = items.indexOf(document.activeElement);
      var next = event.key === "ArrowDown" ? (active + 1) % items.length : (active <= 0 ? items.length - 1 : active - 1);
      items[next].focus();
    });
    input.addEventListener("keydown", function (event) {
      if (event.key === "Enter") {
        var first = box.querySelector("a");
        if (first) { event.preventDefault(); first.click(); }
      }
    });
    document.addEventListener("keydown", function (event) {
      if (event.key !== "/" || dialog.open || event.ctrlKey || event.metaKey || event.altKey) { return; }
      var active = document.activeElement;
      if (active.isContentEditable || /^(input|textarea|select)$/i.test(active.tagName)) { return; }
      if (document.querySelector("dialog[open]")) { return; }
      event.preventDefault(); open();
    });
  }

   

  function boot() {
    initTheme();
    initMode();
    initNav();
    initScrollPadding();
    initTOC();
    initHeadingAnchors();
    initCopyButtons();
    initSmoothScroll();
    initDiagrams();
    initShots();
    initCompare();
    initSearch();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
