{{- define "aspm.collectionStrings" -}}
{{- $selection := .value -}}
{{- $path := .path -}}
{{- range $field := .fields -}}
{{- $value := get $selection $field -}}
{{- if not (kindIs "string" $value) -}}
{{- fail (printf "%s.%s must be a nonempty string" $path $field) -}}
{{- end -}}
{{- if eq (trim $value) "" -}}
{{- fail (printf "%s.%s is required" $path $field) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "aspm.validateCollection" -}}
{{- $collection := .Values.collection -}}
{{- if not (kindIs "map" $collection) -}}
{{- fail "collection must be a mapping" -}}
{{- end -}}
{{- if not (kindIs "bool" $collection.enabled) -}}
{{- fail "collection.enabled must be a boolean" -}}
{{- end -}}
{{- if not (kindIs "map" .Values.core) -}}
{{- fail "core must be a mapping" -}}
{{- end -}}
{{- $storage := $collection.storage -}}
{{- $reader := .Values.core.collectionS3Secret -}}
{{- $publisher := $collection.s3Secret -}}
{{- range $selection := list (dict "value" $storage "path" "collection.storage") (dict "value" $reader "path" "core.collectionS3Secret") (dict "value" $publisher "path" "collection.s3Secret") -}}
{{- if not (kindIs "map" $selection.value) -}}
{{- fail (printf "%s must be a mapping" $selection.path) -}}
{{- end -}}
{{- end -}}
{{- include "aspm.collectionStrings" (dict "value" $collection "path" "collection" "fields" (list "leaseDuration" "githubEndpoint")) -}}
{{- $gateway := $collection.githubEndpoint -}}
{{- if or (not (regexMatch "^https://[^/?#[:space:]@]+" $gateway)) (regexMatch "^https://[^/?#]*@" $gateway) -}}
{{- fail "collection.githubEndpoint must be an HTTPS base without credentials" -}}
{{- end -}}
{{- if or $collection.enabled (gt (len $storage) 0) (gt (len $reader) 0) (gt (len $publisher) 0) -}}
{{- include "aspm.collectionStrings" (dict "value" $storage "path" "collection.storage" "fields" (list "endpoint" "bucket" "prefix" "region")) -}}
{{- $endpoint := $storage.endpoint -}}
{{- if or (not (regexMatch "^https?://[^/?#[:space:]@]+" $endpoint)) (regexMatch "^https?://[^/?#]*@" $endpoint) -}}
{{- fail "collection.storage.endpoint must be an HTTP or HTTPS base without credentials" -}}
{{- end -}}
{{- $fields := list "name" "accessKeyKey" "secretKeyKey" -}}
{{- include "aspm.collectionStrings" (dict "value" $reader "path" "core.collectionS3Secret" "fields" $fields) -}}
{{- if or $collection.enabled (gt (len $publisher) 0) -}}
{{- include "aspm.collectionStrings" (dict "value" $publisher "path" "collection.s3Secret" "fields" $fields) -}}
{{- if and (eq $reader.name $publisher.name) (eq $reader.accessKeyKey $publisher.accessKeyKey) (eq $reader.secretKeyKey $publisher.secretKeyKey) -}}
{{- fail "collection.s3Secret must use a different reader/publisher reference tuple from core.collectionS3Secret" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "aspm.collectionEnvironment" -}}
{{- $storage := .root.Values.collection.storage -}}
{{- if gt (len $storage) 0 -}}
{{- $selected := .root.Values.core.collectionS3Secret -}}
{{- if eq .role "collection" -}}
{{- $selected = .root.Values.collection.s3Secret -}}
{{- end -}}
- name: ASPM_COLLECTION_S3_ENDPOINT
  value: {{ $storage.endpoint | quote }}
- name: ASPM_COLLECTION_S3_BUCKET
  value: {{ $storage.bucket | quote }}
- name: ASPM_COLLECTION_S3_PREFIX
  value: {{ $storage.prefix | quote }}
- name: ASPM_COLLECTION_S3_REGION
  value: {{ $storage.region | quote }}
- name: ASPM_COLLECTION_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef: {name: {{ $selected.name | quote }}, key: {{ $selected.accessKeyKey | quote }}}
- name: ASPM_COLLECTION_S3_SECRET_KEY
  valueFrom:
    secretKeyRef: {name: {{ $selected.name | quote }}, key: {{ $selected.secretKeyKey | quote }}}
{{- end -}}
{{- end -}}
