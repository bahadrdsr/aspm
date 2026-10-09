{{- define "aspm.validateVerification" -}}
{{- $verification := .Values.verification -}}
{{- if not (kindIs "map" $verification) -}}
{{- fail "verification must be a mapping" -}}
{{- end -}}
{{- if not (kindIs "bool" $verification.enabled) -}}
{{- fail "verification.enabled must be a boolean" -}}
{{- end -}}
{{- range $field := list "leaseDuration" "authorizationInterval" -}}
{{- $value := get $verification $field -}}
{{- if not (kindIs "string" $value) -}}
{{- fail (printf "verification.%s must be a nonempty duration string" $field) -}}
{{- end -}}
{{- if eq (trim $value) "" -}}
{{- fail (printf "verification.%s must be a nonempty duration string" $field) -}}
{{- end -}}
{{- end -}}
{{- $fixture := $verification.maxFixtureBytes -}}
{{- $numeric := or (regexMatch "^(u?int(8|16|32|64)?|float(32|64))$" (kindOf $fixture)) (typeIs "json.Number" $fixture) -}}
{{- if not (and $numeric (regexMatch "^[1-9][0-9]*$" (toString $fixture))) -}}
{{- fail "verification.maxFixtureBytes must be an integer number between 1 and 65536" -}}
{{- end -}}
{{- if or (lt (float64 $fixture) 1.0) (gt (float64 $fixture) 65536.0) -}}
{{- fail "verification.maxFixtureBytes must be an integer number between 1 and 65536" -}}
{{- end -}}
{{- end -}}
