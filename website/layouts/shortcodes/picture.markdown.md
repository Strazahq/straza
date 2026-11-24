{{- $src := .Get "dark" | default (.Get "src") -}}
![{{ .Get "alt" }}]({{ strings.TrimRight "/" site.BaseURL }}/{{ strings.TrimLeft "/" $src }}){{ with .Get "caption" }}

{{ . }}{{ end }}
