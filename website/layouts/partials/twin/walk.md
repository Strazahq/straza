{{- /* The walk record of a page's Markdown copy: the same line and folded
detail as the HTML walk record, as plain sentences. */ -}}
{{- with .Params.tested -}}
{{- if and .version .date }}
Walked on {{ .version }} on {{ .date | time.Format "2006-01-02" }}{{ with .except }}, except {{ strings.TrimRight ". " . }}{{ end }}.
{{- with strings.TrimRight ". " (.platform | default "") }} {{ if findRE `^[a-z]` . }}Walked {{ . }}.{{ else }}{{ . }}.{{ end }}{{ end }}
{{- end }}
{{- end }}
{{- with $.Store.Get "shotbuilds" }} Console screenshots show {{ delimit . ", " " and " }}.{{ end }}
