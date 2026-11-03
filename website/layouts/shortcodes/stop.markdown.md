{{- with .Get "group" }}
{{ . }}:
{{ else }}
{{- $text := .Inner | strings.TrimSpace -}}
{{- if eq (.Get "gap") "true" }}
- {{ .Get "label" }} (no page yet): {{ $text }}
{{- else }}
{{- $p := site.GetPage (.Get "page") -}}
{{- $href := printf "%s%sindex.md" (strings.TrimRight "/" site.BaseURL) $p.RelPermalink -}}
{{- with .Get "anchor" }}{{ $href = printf "%s#%s" $href . }}{{ end }}
- [{{ .Get "label" | default $p.LinkTitle }}]({{ $href }}): {{ $text }}
{{- end }}
{{- end }}
