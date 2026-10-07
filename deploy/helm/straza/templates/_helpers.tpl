{{- define "straza.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "straza.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "straza.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "straza.labels" -}}
app.kubernetes.io/name: {{ include "straza.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "straza.selectorLabels" -}}
app.kubernetes.io/name: {{ include "straza.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "straza.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* Bundled backends deploy only under the enterprise profile and only when
     no external one is configured; bring-your-own always wins, so existing
     installs upgrade without growing duplicate StatefulSets. */}}
{{- define "straza.natsBundled" -}}
{{- if and (eq .Values.profile "enterprise") .Values.nats.enabled (not .Values.nats.url) -}}true{{- end -}}
{{- end -}}

{{- define "straza.postgresBundled" -}}
{{- if and (eq .Values.profile "enterprise") .Values.postgres.enabled (not .Values.postgres.dsn) (not .Values.postgres.existingSecret) -}}true{{- end -}}
{{- end -}}

{{/* The event spine is shared across pods (external url or bundled broker).
     Without this, events are embedded per pod: split-brain at >1 replica. */}}
{{- define "straza.eventsShared" -}}
{{- if or .Values.nats.url (include "straza.natsBundled" .) -}}true{{- end -}}
{{- end -}}

{{- define "straza.natsURL" -}}
{{- if .Values.nats.url -}}{{ .Values.nats.url }}{{- else -}}nats://{{ include "straza.fullname" . }}-nats:4222{{- end -}}
{{- end -}}

{{/* server.publicUrl as set by the config FILE lane (configYaml), or empty.
     Parsed with fromYaml, never sniffed with a regex, so approverPublicUrl
     and approverTLS.publicUrl cannot false-positive. Total by design: a
     malformed configYaml (unparseable, or a scalar server: key) yields "",
     meaning no suppression and the derived env IS injected, because a server
     key that is not a map cannot carry a publicUrl to protect, and strazad
     refuses the malformed file at boot anyway (yaml.Unmarshal into the
     Server struct fails, fail closed), so the injected env never governs a
     serving pod. */}}
{{- define "straza.filePublicUrl" -}}
{{- if .Values.configYaml -}}
{{- $cfg := fromYaml .Values.configYaml | default dict -}}
{{- $server := get $cfg "server" | default dict -}}
{{- if kindIs "map" $server -}}
{{- get $server "publicUrl" | default "" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The effective externally reachable base URL, the session-token issuer.
     Never loopback in k8s (strazad's built-in default is 127.0.0.1:8420,
     wrong in every cluster shape): explicit publicUrl wins, else derive the
     ingress host, else the in-cluster Service DNS. A mis-shaped explicit
     value fails the render: helm fail beats a booting pod that can never
     verify a token (strazad enforces the same shape at boot). */}}
{{- define "straza.publicUrl" -}}
{{- if .Values.publicUrl -}}
{{- /* Mirror of strazad's checkBaseURL acceptance, not an approximation of
       it: scheme case-insensitive (url.Parse lowercases, so HTTP:// boots),
       and the authority may not carry @ (embedded credentials), /?# (path/
       query/fragment, which also bans the trailing slash), or whitespace. */ -}}
{{- if not (regexMatch "^(?i:https?)://[^/?#@\\s]+$" .Values.publicUrl) -}}
{{- fail (printf "straza: publicUrl is the session-token issuer, consumed verbatim: an http(s) scheme and host only, with no path, query, fragment, trailing slash, or embedded credentials (user:pass@). Got %q." .Values.publicUrl) -}}
{{- end -}}
{{- .Values.publicUrl -}}
{{- else if .Values.ingress.enabled -}}
{{- printf "%s://%s" (ternary "https" "http" .Values.ingress.tls) .Values.ingress.host -}}
{{- else -}}
{{- printf "%s://%s.%s.svc:%v" (ternary "https" "http" (ne .Values.tls.existingSecret "")) (include "straza.fullname" .) .Release.Namespace .Values.service.port -}}
{{- end -}}
{{- end -}}

{{/* Secret holding the bundled Postgres password at key "password". */}}
{{- define "straza.pgSecretName" -}}
{{- if .Values.postgres.passwordSecret -}}{{ .Values.postgres.passwordSecret }}{{- else -}}{{ include "straza.fullname" . }}-postgres{{- end -}}
{{- end -}}

{{/* The largest replica count this render can reach (HPA ceiling wins). */}}
{{- define "straza.maxReplicas" -}}
{{- if .Values.autoscaling.enabled -}}{{ .Values.autoscaling.maxReplicas }}{{- else -}}{{ .Values.replicaCount }}{{- end -}}
{{- end -}}
