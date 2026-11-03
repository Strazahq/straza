{{ partial "twin/head.md" . }}
{{ partial "twin/body.md" . }}
{{- $base := strings.TrimRight "/" site.BaseURL }}
{{ range .Pages.ByWeight }}
- [{{ .LinkTitle }}]({{ $base }}{{ .RelPermalink }}index.md){{ with .Description }}: {{ . }}{{ end }}
{{- end }}
