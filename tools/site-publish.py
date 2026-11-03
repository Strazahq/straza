#!/usr/bin/env python3
"""Produce the servable copy of the docs site, comment-stripped and tell-scanned.

The docs source keeps HTML comments that cite the files a page derives
from, and a served page must not carry them: whatever serves the site
serves THIS tool's output, never the raw Hugo build.

Usage:  python3 tools/site-publish.py [--src build/site] [--out build/site-public]

Copies every file. Comments never ship: <!--...--> is stripped from *.html
and *.svg, /*...*/ from *.css, *.js, *.mjs, and from inline <script>/<style>
content in pages, plus // comments in *.js/*.mjs. Literal comment text
shown inside code samples is HTML-escaped (&lt;!--) so it never matches,
and the code stripper is string-aware, so a route glob like
/v1/approver/* inside a string literal is never mistaken for a comment.
Markdown is never copied, except the agent skill files under
.well-known/skills and each page's Markdown copy for agents, the index.md
beside its index.html, which is comment-stripped like a page.

After writing, the OUTPUT tree is scanned for publish-blocking tells
(the names of three maintainer files, paths into the repository's docs/
and internal/ trees, planning labels, the internal codename, em dashes,
AI-attribution lines). Pages and static assets alike: every
HTML, SVG, script and style file in the tree, so a diagram or a replay under
the site's static directory is held to the same rules as a page. Any finding
prints as file:line [rule] and the tool exits 2: a red tree must not be
served. There is no bypass flag; the sanctioned-use allowlist below is the
only exemption mechanism, every entry names a specific, deliberate usage
(each one a typographic design convention), and an entry exempts only its
own matches, never the rest of the line.
"""

import argparse
import pathlib
import re
import shutil
import sys

COMMENT = re.compile(r"<!--.*?-->\n?", re.DOTALL)
INLINE_SCRIPT = re.compile(r"(<script\b[^>]*>)(.*?)(</script>)", re.DOTALL | re.IGNORECASE)
INLINE_STYLE = re.compile(r"(<style\b[^>]*>)(.*?)(</style>)", re.DOTALL | re.IGNORECASE)


def strip_code_comments(src: str, line_comments: bool) -> str:
    """Remove /*...*/ (and, for JS, whole-line //) comments, string-aware.

    A character walk, not a regex: JS/CSS strings legitimately contain /*
    (route globs like /v1/approver/*), which a regex would eat to the next
    */ and corrupt the script. Strings ('..', "..", `..`) pass through
    verbatim, honoring backslash escapes. JS regex literals are not parsed;
    the site's code keeps patterns in strings, and the output is
    syntax-checked whenever this tool's own tests run.
    """
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c in ("'", '"', "`"):
            q = c
            out.append(c)
            i += 1
            while i < n:
                out.append(src[i])
                if src[i] == "\\" and i + 1 < n:
                    out.append(src[i + 1])
                    i += 2
                    continue
                if src[i] == q:
                    i += 1
                    break
                i += 1
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "*":
            end = src.find("*/", i + 2)
            if end == -1:
                out.append(src[i:])
                break
            out.append(" ")
            i = end + 2
            continue
        if line_comments and c == "/" and i + 1 < n and src[i + 1] == "/":
            eol = src.find("\n", i)
            if eol == -1:
                break
            i = eol
            continue
        out.append(c)
        i += 1
    return "".join(out)

# Publish-blocking tells, applied per line of every emitted text file. The em
# dash and the codename are written as escapes, so this file carries neither.
# The codename rule matches inside a word too, because an identifier that
# joins it to another word is the form that slips past a word-bounded check.
RULES = [
    ("internal-doc-path", re.compile(
        r"CLAUDE\.md|PLAN\.md|STATE\.md"
        # A bare path into the repository's docs/ or Go tree, the form a
        # diagram citation leaked; a URL path (/docs/, straza/internal/version)
        # and a hostname (straza.internal/) are preceded by a slash or a dot.
        r"|(?<![\w./-])(?:docs/[\w.-]|internal/[a-z])")),
    ("decision-token", re.compile(r"\((?:D|T)\d{1,2}\)|\bPLAN\s*§")),
    ("codename", re.compile("\x77arden", re.IGNORECASE)),
    ("em-dash", re.compile("\u2014|&#8212;")),
    ("ai-attribution", re.compile(r"Claude-Session|Generated with Claude")),
]

# Sanctioned uses. An ALLOW entry removes its own matches from the line
# before the named rule runs (rule=None exempts the whole line from every
# rule), so a sanctioned use never hides a second occurrence beside it: the
# diagrams' fact strings are long script lines, and a prose em dash next to
# a citation slipped through a whole-line exemption. Keep entries specific:
# a broad pattern here silently re-opens the hole the gate exists to close.
ALLOW = [
    # Diagram source-citation prefix and the tooltip renderer separator:
    # typographic conventions, not prose.
    ("em-dash", re.compile(r"\+f\.b\+")),
    # Empty table cells render a lone dash as a placeholder.
    ("em-dash", re.compile("<td>\u2014</td>")),
]


def scan(out: pathlib.Path) -> int:
    findings = 0
    for path in sorted(out.rglob("*")):
        if path.is_dir() or path.suffix.lower() not in (".html", ".htm", ".js", ".mjs", ".css", ".svg", ".md", ".json", ".yaml", ".sh"):
            continue
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            exempt_all = any(rule is None and rx.search(line) for rule, rx in ALLOW)
            if exempt_all:
                continue
            for rule, rx in RULES:
                probe = line
                for a_rule, a_rx in ALLOW:
                    if a_rule == rule:
                        probe = a_rx.sub("", probe)
                if not rx.search(probe):
                    continue
                findings += 1
                print(f"site-publish: TELL {path.relative_to(out)}:{lineno} [{rule}] {line.strip()[:120]}",
                      file=sys.stderr)
    return findings


FENCE = re.compile(r"(?ms)^[ \t]*(```|~~~).*?^[ \t]*\1[ \t]*$")
CODE_SPAN = re.compile(r"`[^`\n]*`")
LEFTOVER = re.compile(r"\{\{[<%]|<!--|<(?:div|figure|svg|details|summary|span|ol|li|dl|table|section|nav)\b")


def check_twins(out: pathlib.Path) -> int:
    """Every page is served as Markdown for agents.

    An index.html that is a page, not the redirect left at a moved page's
    old address, needs its index.md beside it. Outside code, the copy may
    carry no shortcode, no comment and no HTML block, which is what a
    shortcode without a Markdown form leaves behind.
    """
    findings = 0
    for page in sorted(out.rglob("index.html")):
        if 'http-equiv="refresh"' in page.read_text(encoding="utf-8", errors="replace"):
            continue
        twin = page.with_name("index.md")
        rel = twin.relative_to(out)
        if not twin.is_file():
            print(f"{rel}: [markdown] missing: every page is served as Markdown for agents, "
                  "so add the markdown output for this kind of page in website/hugo.toml",
                  file=sys.stderr)
            findings += 1
            continue
        text = twin.read_text(encoding="utf-8")
        text = FENCE.sub(lambda m: "\n" * m.group(0).count("\n"), text)
        for n, line in enumerate(text.split("\n"), 1):
            m = LEFTOVER.search(CODE_SPAN.sub("", line))
            if m:
                print(f"{rel}:{n}: [markdown] {m.group(0)!r} is left in the Markdown copy: give the "
                      "shortcode that writes it a Markdown form beside its HTML one",
                      file=sys.stderr)
                findings += 1
    return findings


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--src", default="build/site", type=pathlib.Path)
    ap.add_argument("--out", default="build/site-public", type=pathlib.Path)
    args = ap.parse_args()

    if not args.src.is_dir():
        print(f"site-publish: source {args.src} is not a directory", file=sys.stderr)
        return 1
    if args.out.exists():
        shutil.rmtree(args.out)

    pages = stripped = 0
    for path in sorted(args.src.rglob("*")):
        if path.is_dir():
            continue
        rel = path.relative_to(args.src)
        # Markdown is never served, with two exceptions. The agent skill
        # files under .well-known/skills are SKILL.md by the Agent Skills spec
        # and are copied verbatim. A page's Markdown copy for agents is the
        # index.md that Hugo writes beside its index.html, and it
        # is comment-stripped like the page. Both are held to the tell scan.
        twin = path.name == "index.md" and (path.parent / "index.html").is_file()
        if path.suffix.lower() == ".md" and rel.parts[:2] != (".well-known", "skills") and not twin:
            continue
        # diagrams/alt/ is the styling workbench (its gallery page says so
        # itself): comparison material, never part of the served site.
        if rel.parts[:2] == ("diagrams", "alt"):
            continue
        dest = args.out / rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        suffix = path.suffix.lower()
        if suffix in (".html", ".htm"):
            text = path.read_text(encoding="utf-8")
            clean, n = COMMENT.subn("", text)
            clean = INLINE_SCRIPT.sub(
                lambda m: m.group(1) + strip_code_comments(m.group(2), line_comments=False) + m.group(3), clean)
            clean = INLINE_STYLE.sub(
                lambda m: m.group(1) + strip_code_comments(m.group(2), line_comments=False) + m.group(3), clean)
            dest.write_text(clean, encoding="utf-8")
            pages += 1
            stripped += n
        elif suffix == ".svg" or twin:
            dest.write_text(COMMENT.sub("", path.read_text(encoding="utf-8")), encoding="utf-8")
        elif suffix == ".css":
            dest.write_text(strip_code_comments(path.read_text(encoding="utf-8"), line_comments=False),
                            encoding="utf-8")
        elif suffix in (".js", ".mjs"):
            dest.write_text(strip_code_comments(path.read_text(encoding="utf-8"), line_comments=True),
                            encoding="utf-8")
        else:
            shutil.copy2(path, dest)

    findings = scan(args.out) + check_twins(args.out)
    if findings:
        print(f"site-publish: {findings} tell(s) in {args.out}: DO NOT SERVE this tree",
              file=sys.stderr)
        return 2

    print(f"site-publish: {pages} pages -> {args.out}, {stripped} comments stripped, tell-scan clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())
