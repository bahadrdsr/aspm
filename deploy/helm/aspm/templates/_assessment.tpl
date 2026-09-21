{{- define "aspm.validateAssessment" -}}
{{- $assessment := .Values.assessment -}}
{{- if not (kindIs "map" $assessment) -}}
{{- fail "assessment must be a mapping" -}}
{{- end -}}
{{- if not (kindIs "bool" $assessment.enabled) -}}
{{- fail "assessment.enabled must be a boolean" -}}
{{- end -}}
{{- $scope := $assessment.scope -}}
{{- if not (kindIs "string" $scope) -}}
{{- fail "assessment.scope must be a string" -}}
{{- end -}}
{{- if and $assessment.enabled (eq $scope "") -}}
{{- fail "assessment.scope is required when assessment.enabled is true" -}}
{{- end -}}
{{- if or (ne $scope (trim $scope)) (regexMatch "\\p{Cc}" $scope) (gt (len $scope) 128) -}}
{{- fail "assessment.scope must have no surrounding whitespace or control characters and be at most 128 UTF-8 bytes" -}}
{{- end -}}
{{/* JSON round-tripping detects invalid UTF-8 without restricting valid Unicode. */}}
{{- $decoded := fromJson (mustToJson (dict "scope" $scope)) -}}
{{- if ne $scope $decoded.scope -}}
{{- fail "assessment.scope must be valid UTF-8" -}}
{{- end -}}
{{- range $field := list "leaseDuration" "authorizationInterval" "requestTimeout" "requestWindow" -}}
{{- $value := get $assessment $field -}}
{{- if not (kindIs "string" $value) -}}
{{- fail (printf "assessment.%s must be a nonempty duration string" $field) -}}
{{- end -}}
{{- if eq (trim $value) "" -}}
{{- fail (printf "assessment.%s must be a nonempty duration string" $field) -}}
{{- end -}}
{{- end -}}
{{- range $field, $maximum := dict "maxConcurrent" 16 "requestsPerWindow" 1000 "maxInputBytes" 32768 "maxOutputTokens" 32768 "maxResponseBytes" 131072 -}}
{{- $value := get $assessment $field -}}
{{- $numeric := or (regexMatch "^(u?int(8|16|32|64)?|float(32|64))$" (kindOf $value)) (typeIs "json.Number" $value) -}}
{{- if not (and $numeric (regexMatch "^[1-9][0-9]*$" (toString $value))) -}}
{{- fail (printf "assessment.%s must be an integer number between 1 and %d" $field $maximum) -}}
{{- end -}}
{{- if or (lt (float64 $value) 1.0) (gt (float64 $value) (float64 $maximum)) -}}
{{- fail (printf "assessment.%s must be an integer number between 1 and %d" $field $maximum) -}}
{{- end -}}
{{- end -}}
{{- end -}}
