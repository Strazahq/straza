{{- $base := strings.TrimRight "/" site.BaseURL -}}
# {{ .Title }}

{{ .Description }}

{{ partial "twin/body.md" . }}

## Pick your path
{{ range .Params.paths }}
{{- $j := site.GetPage .journey }}
### {{ .title }}

Who it is for: {{ .who }}. The journey: [{{ $j.LinkTitle }}]({{ $base }}{{ $j.RelPermalink }}index.md).
{{ range $i, $e := .pages }}{{ $p := site.GetPage $e.page }}
{{ add $i 1 }}. [{{ $e.label | default $p.LinkTitle }}]({{ $base }}{{ $p.RelPermalink }}index.md)
{{- end }}
{{ end }}
## Already running Straza
{{ range .Params.quick }}{{ $p := site.GetPage .page }}
- [{{ .label | default $p.LinkTitle }}]({{ $base }}{{ $p.RelPermalink }}index.md)
{{- end }}

Every page of these docs has a Markdown copy at its address plus index.md. The full list is at {{ $base }}/llms.txt.
