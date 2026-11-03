| What differs | Standalone | Enterprise |
|---|---|---|
{{- range hugo.Data.profiles }}
| **{{ .title }}** | | |
{{- range .rows }}
| {{ .what }}{{ with .knob }} (`{{ . }}`){{ end }} | {{ .standalone }} | {{ .enterprise }} |
{{- end }}
{{- end }}
