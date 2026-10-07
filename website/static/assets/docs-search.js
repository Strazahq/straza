 
(function (scope) {
  "use strict";

  function score(entry, tokens) {
    var title = entry.t.toLowerCase();
    var page = entry.pt.toLowerCase();
    var keys = (entry.k || "").toLowerCase();
    var body = (entry.b || "").toLowerCase();
    var total = 0;
    for (var i = 0; i < tokens.length; i++) {
      var token = tokens[i];
      if (title === token) { total += 12; }
      else if (title.indexOf(token) === 0) { total += 8; }
      else if (title.indexOf(token) !== -1) { total += 6; }
      else if (page.indexOf(token) !== -1) { total += 4; }
      else if (keys.indexOf(token) !== -1) { total += 3; }
      else if (body.indexOf(token) !== -1) { total += 1; }
      else { return -1; }
    }
    return total;
  }

  function snippet(body, tokens) {
    if (!body) { return ""; }
    var lower = body.toLowerCase();
    var bestStart = 0;
    var bestScore = -1;
    var length = 225;
    for (var i = 0; i < tokens.length; i++) {
      var at = lower.indexOf(tokens[i]);
      while (at >= 0) {
        var start = Math.max(0, at - 55);
        var window = lower.slice(start, start + length);
        var score = 0;
        for (var j = 0; j < tokens.length; j++) {
          if (window.indexOf(tokens[j]) >= 0) { score += 10; }
        }
        if (window.indexOf(tokens.join(" ")) >= 0) { score += 5; }
        if (score > bestScore) { bestScore = score; bestStart = start; }
        at = lower.indexOf(tokens[i], at + Math.max(1, tokens[i].length));
      }
    }
    if (bestStart > 0) {
      var boundary = body.indexOf(" ", bestStart);
      if (boundary >= 0 && boundary < bestStart + 25) { bestStart = boundary + 1; }
    }
    var end = Math.min(body.length, bestStart + length);
    if (end < body.length) {
      var lastSpace = body.lastIndexOf(" ", end);
      if (lastSpace > end - 25) { end = lastSpace; }
    }
    return (bestStart ? "…" : "") + body.slice(bestStart, end).trim() + (end < body.length ? "…" : "");
  }

  function search(index, query, limit, section) {
    var q = query.trim().toLowerCase();
    if (q.length < 2) { return { total: 0, hits: [] }; }
    var tokens = q.split(/\s+/);
    var pages = Object.create(null);
    var best = Object.create(null);
    for (var i = 0; i < index.length; i++) {
      var entry = index[i];
      if (section && entry.p.split("/")[0] !== section) { continue; }
      if (entry.b) { pages[entry.p] = entry.b; }
      var rank = score(entry, tokens);
      if (rank >= 0 && (!best[entry.p] || rank > best[entry.p].score)) {
        best[entry.p] = { entry: entry, score: rank };
      }
    }
    var hits = Object.keys(best).map(function (path) {
      var hit = best[path];
      hit.snippet = snippet(pages[path] || "", tokens);
      return hit;
    });
    hits.sort(function (a, b) {
      return b.score - a.score || a.entry.pt.localeCompare(b.entry.pt) || a.entry.p.localeCompare(b.entry.p);
    });
    return { total: hits.length, hits: hits.slice(0, limit || 10) };
  }

  if (typeof module !== "undefined" && module.exports) { module.exports = search; }
  else { scope.StrazaDocsSearch = search; }
})(typeof window !== "undefined" ? window : this);
