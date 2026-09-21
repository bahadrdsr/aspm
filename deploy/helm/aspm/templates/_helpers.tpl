{{- define "aspm.name" -}}
{{- printf "%s-aspm" .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- define "aspm.validateDelivery" -}}
{{- $delivery := dict -}}
{{- if hasKey .Values "delivery" -}}
{{- if not (kindIs "map" .Values.delivery) -}}
{{- fail "delivery must be a mapping" -}}
{{- end -}}
{{- $delivery = .Values.delivery -}}
{{- end -}}
{{- $enabled := false -}}
{{- if hasKey $delivery "enabled" -}}
{{- if not (kindIs "bool" $delivery.enabled) -}}
{{- fail "delivery.enabled must be a boolean" -}}
{{- end -}}
{{- $enabled = $delivery.enabled -}}
{{- end -}}
{{- $key := dict -}}
{{- if hasKey .Values "integrationKeySecret" -}}
{{- if not (kindIs "map" .Values.integrationKeySecret) -}}
{{- fail "integrationKeySecret must be a mapping with name and key" -}}
{{- end -}}
{{- $key = .Values.integrationKeySecret -}}
{{- end -}}
{{- if or $enabled .Values.collection.enabled (gt (len $key) 0) -}}
{{- range $field := list "name" "key" -}}
{{- $value := get $key $field -}}
{{- if not (kindIs "string" $value) -}}
{{- fail (printf "integrationKeySecret.%s must be a nonempty string" $field) -}}
{{- end -}}
{{- if eq (trim $value) "" -}}
{{- fail (printf "integrationKeySecret.%s is required" $field) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $field := list "leaseDuration" "slackEndpoint" -}}
{{- if or $enabled (hasKey $delivery $field) -}}
{{- $value := get $delivery $field -}}
{{- if not (kindIs "string" $value) -}}
{{- fail (printf "delivery.%s must be a nonempty string" $field) -}}
{{- end -}}
{{- if eq (trim $value) "" -}}
{{- fail (printf "delivery.%s must be a nonempty string" $field) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if or $enabled (hasKey $delivery "slackEndpoint") -}}
{{- $endpoint := get $delivery "slackEndpoint" -}}
{{- if or (not (hasPrefix "https://" $endpoint)) (not (regexMatch "^https://[^/?#[:space:]@]+" $endpoint)) (regexMatch "^https://[^/?#]*@" $endpoint) -}}
{{- fail "delivery.slackEndpoint must be an HTTPS base without credentials" -}}
{{- end -}}
{{- end -}}
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
{{- if or (eq $role "core") (eq $role "delivery") (eq $role "collection") (eq $role "assessment") }}
{{- $integrationKey := $root.Values.integrationKeySecret | default dict }}
{{- if gt (len $integrationKey) 0 }}
- name: ASPM_INTEGRATION_ENCRYPTION_KEY
  valueFrom:
    secretKeyRef: {name: {{ $integrationKey.name | quote }}, key: {{ $integrationKey.key | quote }}}
{{- end }}
{{- end }}
{{- if and (or (eq $role "core") (eq $role "assessment")) (ne $root.Values.assessment.scope "") }}
- name: ASPM_ASSESSMENT_SCOPE
  value: {{ $root.Values.assessment.scope | quote }}
{{- end }}
{{- if eq $role "assessment" }}
- name: ASPM_LISTEN
  value: "0.0.0.0:8080"
- name: ASPM_ASSESSMENT_LEASE_DURATION
  value: {{ $settings.leaseDuration | quote }}
- name: ASPM_ASSESSMENT_AUTHORIZATION_INTERVAL
  value: {{ $settings.authorizationInterval | quote }}
- name: ASPM_ASSESSMENT_REQUEST_TIMEOUT
  value: {{ $settings.requestTimeout | quote }}
- name: ASPM_ASSESSMENT_REQUEST_WINDOW
  value: {{ $settings.requestWindow | quote }}
- name: ASPM_ASSESSMENT_MAX_CONCURRENT
  value: {{ $settings.maxConcurrent | quote }}
- name: ASPM_ASSESSMENT_REQUESTS_PER_WINDOW
  value: {{ $settings.requestsPerWindow | quote }}
- name: ASPM_ASSESSMENT_MAX_INPUT_BYTES
  value: {{ $settings.maxInputBytes | quote }}
- name: ASPM_ASSESSMENT_MAX_OUTPUT_TOKENS
  value: {{ $settings.maxOutputTokens | quote }}
- name: ASPM_ASSESSMENT_MAX_RESPONSE_BYTES
  value: {{ $settings.maxResponseBytes | quote }}
{{- end }}
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
{{- if eq $role "delivery" }}
- name: ASPM_DELIVERY_LEASE_DURATION
  value: {{ $settings.leaseDuration | quote }}
- name: ASPM_SLACK_ENDPOINT
  value: {{ $settings.slackEndpoint | quote }}
{{- end }}
{{- if or (eq $role "core") (eq $role "collection") }}
{{ include "aspm.collectionEnvironment" . }}
{{- end }}
{{- if eq $role "collection" }}
- name: ASPM_COLLECTION_LEASE_DURATION
  value: {{ $settings.leaseDuration | quote }}
- name: ASPM_GITHUB_ENDPOINT
  value: {{ $settings.githubEndpoint | quote }}
{{- end }}
{{- if or (eq $role "core") (eq $role "ingestion") }}
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
