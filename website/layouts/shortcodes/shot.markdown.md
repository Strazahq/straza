{{- $s := index (hugo.Data.shots | default dict) (.Get "name") -}}
{{- with $s.build }}{{ $.Page.Store.Set "shotbuilds" (union ($.Page.Store.Get "shotbuilds" | default slice) (slice .)) }}{{ end -}}
![{{ $s.alt }}]({{ strings.TrimRight "/" site.BaseURL }}/{{ strings.TrimLeft "/" $s.dark }}){{ with .Get "caption" }}

{{ . }}{{ end }}
