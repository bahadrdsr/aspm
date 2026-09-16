{{- define "aspm.name" -}}
{{- printf "%s-aspm" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- define "aspm.environment" -}}
{{- $root := .root -}}
{{- $role := .role -}}
{{- $settings := index $root.Values $role -}}
- name: ASPM_DATABASE_URL
  valueFrom:
    secretKeyRef: {name: {{ $root.Values.existingSecret | quote }}, key: database-url}
- name: ASPM_SCHEMA
  value: {{ $root.Values.database.schema | quote }}
- name: ASPM_DB_MAX_CONNECTIONS
  value: {{ $root.Values.database.maxConnectionsPerReplica | quote }}
{{- if eq $role "core" }}
{{- if not (kindIs "bool" $settings.prepareReadiness) }}
{{- fail "core.prepareReadiness must be a boolean" }}
{{- end }}
- name: ASPM_S3_PREPARE_READINESS
  value: {{ $settings.prepareReadiness | quote }}
- name: ASPM_PUBLIC_ORIGIN
  value: {{ if $root.Values.ingress.enabled }}{{ printf "https://%s" $root.Values.ingress.host | quote }}{{ else }}{{ $root.Values.publicOrigin | default "" | quote }}{{ end }}
- name: ASPM_BOOTSTRAP_TOKEN
  valueFrom:
    secretKeyRef: {name: {{ $root.Values.existingSecret | quote }}, key: bootstrap-token, optional: true}
{{- end }}
{{- if ne $role "reports" }}
{{- $selected := $settings.s3Secret | default dict }}
{{- $secretName := required (printf "%s.s3Secret.name is required" $role) $selected.name }}
{{- $accessKey := required (printf "%s.s3Secret.accessKeyKey is required" $role) $selected.accessKeyKey }}
{{- $secretKey := required (printf "%s.s3Secret.secretKeyKey is required" $role) $selected.secretKeyKey }}
- name: ASPM_S3_ENDPOINT
  value: {{ if $root.Values.storage.managed }}{{ printf "http://%s-storage:8333" (include "aspm.name" $root) | quote }}{{ else }}{{ required "storage.endpoint is required for external S3" $root.Values.storage.endpoint | quote }}{{ end }}
- name: ASPM_S3_BUCKET
  value: {{ $root.Values.storage.bucket | quote }}
- name: ASPM_S3_PREFIX
  value: {{ required (printf "%s.rawPrefix is required" $role) $settings.rawPrefix | quote }}
- name: ASPM_S3_READINESS_KEY
  value: {{ required (printf "%s.readinessKey is required" $role) $settings.readinessKey | quote }}
- name: ASPM_S3_REGION
  value: {{ $root.Values.storage.region | quote }}
- name: ASPM_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef: {name: {{ $secretName | quote }}, key: {{ $accessKey | quote }}}
- name: ASPM_S3_SECRET_KEY
  valueFrom:
    secretKeyRef: {name: {{ $secretName | quote }}, key: {{ $secretKey | quote }}}
{{- if eq $role "ingestion" }}
- name: ASPM_S3_NORMALIZED_PREFIX
  value: {{ required "ingestion.normalizedPrefix is required" $settings.normalizedPrefix | quote }}
{{- end }}
{{- end }}
{{- end -}}
