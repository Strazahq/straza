{{- $p := site.GetPage (.Get 0) -}}
Next: [{{ $p.LinkTitle }}]({{ strings.TrimRight "/" site.BaseURL }}{{ $p.RelPermalink }}index.md)
