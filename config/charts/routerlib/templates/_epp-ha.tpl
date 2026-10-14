{{/* Resolve HA settings once for the EPP arguments and lease RBAC. */}}
{{- define "llm-d-router.epp.flags" -}}
{{- $epp := .Values.router.epp -}}
{{- $flags := deepCopy ($epp.flags | default dict) -}}
{{- $mode := $epp.haMode | default "" -}}
{{- $crossReplica := $epp.crossReplica | default dict -}}
{{- $autoscaling := $epp.autoscaling | default dict -}}
{{- $gkePB := include "llm-d-router.gkePreferredBackends" . | fromYaml | default dict -}}
{{- $priorityRouting := eq (include "llm-d-router.priorityRouting.enabled" .) "true" -}}
{{- if not (has $mode (list "" "leader-elected" "all-active")) -}}
  {{- fail "router.epp.haMode must be empty, leader-elected, or all-active" -}}
{{- end -}}
{{- if $crossReplica.enabled -}}
  {{- if eq $mode "leader-elected" -}}
    {{- fail "router.epp.crossReplica.enabled requires all-active mode" -}}
  {{- end -}}
  {{- $mode = "all-active" -}}
  {{- if and (hasKey $flags "allow-experimental-plugins") (not (regexMatch "^(1|t|T|true|TRUE|True)$" (toString (index $flags "allow-experimental-plugins")))) -}}
    {{- fail "router.epp.crossReplica.enabled requires allow-experimental-plugins=true" -}}
  {{- end -}}
  {{- $_ := set $flags "allow-experimental-plugins" true -}}
  {{- if or (hasKey $flags "config-file") (hasKey $flags "config-text") -}}
    {{- fail "router.epp.crossReplica.enabled cannot override config-file or config-text; use pluginsConfig or pluginsCustomConfig" -}}
  {{- end -}}
{{- end -}}
{{- if and $mode (or $gkePB.enabled $priorityRouting) -}}
  {{- fail "router.epp.haMode is incompatible with priority routing and GKE preferredBackends" -}}
{{- end -}}
{{- if and (eq $mode "leader-elected") $autoscaling.enabled -}}
  {{- fail "EPP autoscaling requires active-active replicas" -}}
{{- end -}}
{{- if $mode -}}
  {{- $leaderElection := eq $mode "leader-elected" -}}
  {{- if and (hasKey $flags "ha-enable-leader-election") (ne $leaderElection (regexMatch "^(1|t|T|true|TRUE|True)$" (toString (index $flags "ha-enable-leader-election")))) -}}
    {{- fail "router.epp.flags.ha-enable-leader-election conflicts with router.epp.haMode (crossReplica.enabled selects all-active)" -}}
  {{- end -}}
  {{- $_ := set $flags "ha-enable-leader-election" $leaderElection -}}
{{- else if and (not (hasKey $flags "ha-enable-leader-election")) (not $autoscaling.enabled) (gt ($epp.replicas | int) 1) (not $gkePB.enabled) (not $priorityRouting) -}}
  {{- $_ := set $flags "ha-enable-leader-election" true -}}
{{- end -}}
{{- toYaml $flags -}}
{{- end -}}
