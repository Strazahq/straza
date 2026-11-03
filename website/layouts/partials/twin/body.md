{{- /* The body of a page's Markdown copy: the source with its shortcodes
rendered in their Markdown forms, with the source comments, the heading
attributes and the span tags of chips and knobs removed, and with every link
into the docs made absolute. A link to a page goes to that page's Markdown
copy, so an agent stays in Markdown. */ -}}
{{- $base := strings.TrimRight "/" site.BaseURL -}}
{{- $b := .RenderShortcodes -}}
{{- $b = $b | replaceRE `(?s)[ \t]*\n?` "" -}}
{{- $b = $b | replaceRE `(?m)^(#{1,6} .*?)[ \t]*\{[^}\n]*\}[ \t]*$` "$1" -}}
{{- $b = $b | replaceRE `<span\b[^>]*>([^<]*)</span>` "$1" -}}
{{- $b = $b | replaceRE `\]\(/((?:[^)#\s]*/)?)(#[^)\s]*)?\)` (printf "](%s/${1}index.md${2})" $base) -}}
{{- $b = $b | replaceRE `\]\(/([^)\s]*)\)` (printf "](%s/${1})" $base) -}}
{{- $b = $b | replaceRE `\n{3,}` "\n\n" -}}
{{ strings.TrimSpace $b }}
