{{- /* The body of a page's Markdown copy: the source with its shortcodes
rendered in their Markdown forms, with the source comments, the heading
attributes and the span tags of chips and knobs removed, and with every link
into the docs made absolute. A link to a page goes to that page's Markdown
copy, so an agent stays in Markdown. */ -}}
{{- $base := strings.TrimRight "/" site.BaseURL -}}
{{- $b := .RenderShortcodes -}}
{{- /* The comment markers spell their dashes as -{2}, because the release
assembler cuts each HTML comment out of a shipped .md file, this one
included, and would take a literal marker here for the start of one. */ -}}
{{- $b = $b | replaceRE `(?s)<!-{2}.*?-{2}>[ \t]*\n?` "" -}}
{{- $b = $b | replaceRE `(?m)^(#{1,6} .*?)[ \t]*\{[^}\n]*\}[ \t]*$` "$1" -}}
{{- $b = $b | replaceRE `<span\b[^>]*>([^<]*)</span>` "$1" -}}
{{- $b = $b | replaceRE `\]\(/((?:[^)#\s]*/)?)(#[^)\s]*)?\)` (printf "](%s/${1}index.md${2})" $base) -}}
{{- $b = $b | replaceRE `\]\(/([^)\s]*)\)` (printf "](%s/${1})" $base) -}}
{{- $b = $b | replaceRE `\n{3,}` "\n\n" -}}
{{ strings.TrimSpace $b }}
