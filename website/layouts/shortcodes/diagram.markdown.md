{{- /* A diagram in a page's Markdown copy: its caption, the sentence of
its SVG title, and its steps as a list when it has them. */ -}}
{{- $svg := partial (printf "diagrams/%s.html" (.Get "name")) .Page -}}
{{- $title := "" -}}
{{- with findRESubmatch `(?s)<title[^>]*>(.*?)</title>` $svg 1 }}{{ $title = index (index . 0) 1 }}{{ end -}}
{{- $say := "" -}}
{{- with findRESubmatch `data-say='([^']*)'` $svg 1 }}{{ $say = index (index . 0) 1 }}{{ end -}}
Diagram{{ with .Get "caption" }}: {{ . }}{{ end }}
{{ with $title }}
{{ . | htmlUnescape }}
{{ end }}
{{- with $say }}{{ range after 1 (findRESubmatch `"((?:[^"\\]|\\.)*)"` (htmlUnescape .)) }}
{{ index . 1 | replaceRE `\\(.)` "$1" }}
{{- end }}{{ end }}
