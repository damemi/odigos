{{- define "utils.imagePrefix" -}}
{{- $odigosRegistry := "registry.odigos.io" -}}
{{- $redHatRegistry := "registry.connect.redhat.com/odigos" -}}
{{- if $.Values.imagePrefix -}}
    {{- $.Values.imagePrefix -}}
{{- else if $.Values.openshift.enabled -}}
    {{- $redHatRegistry -}}
{{- else -}}
    {{- $odigosRegistry -}}
{{- end -}}
{{- end -}}

{{- define "odigos.onPremToken" -}}
{{- if .Values.onPremToken -}}
{{- .Values.onPremToken -}}
{{- else -}}
{{- $sec := lookup "v1" "Secret" .Release.Namespace "odigos-pro" -}}
{{- if $sec -}}
{{- if index $sec.data "odigos-onprem-token" -}}
{{- index $sec.data "odigos-onprem-token" | b64dec -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "odigos.enterpriseRegistryPullSecretName" -}}
odigos-enterprise-registry
{{- end -}}

{{- define "odigos.createEnterpriseRegistryPullSecret" -}}
{{- if and (include "odigos.usesOdigosRegistry" .) (include "odigos.onPremToken" .) (not (.Values.externalOnpremPullSecret | default false)) -}}
true
{{- end -}}
{{- end -}}

{{- define "odigos.usesOdigosRegistry" -}}
{{- if eq (include "utils.imagePrefix" .) "registry.odigos.io" -}}
true
{{- end -}}
{{- end -}}

{{- define "odigos.enterpriseRegistryPullSecretToMount" -}}
{{- if include "odigos.usesOdigosRegistry" . -}}
{{- include "odigos.enterpriseRegistryPullSecretName" . -}}
{{- end -}}
{{- end -}}

{{- define "odigos.validateEnterpriseRegistryPullSecrets" -}}
{{- if and (include "odigos.secretExists" .) (include "odigos.usesOdigosRegistry" .) (not (include "odigos.onPremToken" .)) (not (.Values.externalOnpremPullSecret | default false)) -}}
{{- fail "Odigos images pull from registry.odigos.io but no on-prem token is available to the chart. Set onPremToken, set externalOnpremPullSecret to true when providing odigos-enterprise-registry externally, or ensure the odigos-pro secret exists in the release namespace before install/upgrade." -}}
{{- end -}}
{{- end -}}

{{- define "odigos.enterpriseRegistryDockerConfigJson" -}}
{{- $token := include "odigos.onPremToken" . -}}
{{- $auth := printf "odigos:%s" $token | b64enc -}}
{{- dict "auths" (dict "registry.odigos.io" (dict "username" "odigos" "password" $token "auth" $auth)) | toJson -}}
{{- end -}}

{{- define "utils.imageName" -}}
{{- /* Check for component-specific image override in .Values.images.<component> */ -}}
{{- $images := $.Values.images | default dict -}}
{{- $componentImage := get $images .Component -}}
{{- if $componentImage -}}
  {{- $componentImage -}}
{{- else -}}
  {{- $certified := $.Values.openshift.enabled }}
  {{- if hasKey $.Values.openshift "certifiedImageTags" }}
    {{- $certified = $.Values.openshift.certifiedImageTags }}
  {{- end }}
  {{- $prefix := include "utils.imagePrefix" . -}}
  {{- printf "%s/odigos-%s%s:%s" $prefix .Component (ternary "-rhel-certified" "" (and $.Values.openshift.enabled $certified)) .Tag }}
{{- end -}}
{{- end -}}
{{/*
Returns "true" if any userInstrumentationEnvs.language is enabled or has env vars
*/}}
{{- define "utils.shouldRenderUserInstrumentationEnvs" -}}
  {{- $languages := .Values.userInstrumentationEnvs.languages | default dict }}
  {{- $shouldRender := false }}
  {{- range $lang, $config := $languages }}
    {{- if or $config.enabled $config.env }}
      {{- $shouldRender = true }}
    {{- end }}
  {{- end }}
  {{- print $shouldRender }}
{{- end }}

{{- define "odigos.secretExists" -}}
  {{- $sec   := lookup "v1" "Secret" .Release.Namespace "odigos-pro" -}}
  {{- $token := default "" .Values.onPremToken -}}
  {{- $externalSecret := .Values.externalOnpremTokenSecret | default false -}}
  {{- if or $sec (ne $token "") $externalSecret -}}
true
  {{- end -}}
{{- end -}}

{{/*
  Autopilot is enabled by insights.autopilot.enabled. That flag
  requires insights.enabled=true and implies the insights profile (see
  odigos.validateAutopilotDeps).
*/}}
{{- define "odigos.autopilotEnabled" -}}
{{- if .Values.insights.autopilot.enabled -}}
true
{{- end -}}
{{- end -}}

{{/*
  Insights is on when insights.enabled is true.
*/}}
{{- define "odigos.insightsEnabled" -}}
{{- if .Values.insights.enabled -}}
true
{{- end -}}
{{- end -}}

{{/*
  Bundled ClickHouse backs insights (including autopilot).
*/}}
{{- define "odigos.clickhouseEnabled" -}}
{{- include "odigos.insightsEnabled" . -}}
{{- end -}}

{{/*
  Profiling is on when profiling.enabled is true.
*/}}
{{- define "odigos.profilingEnabled" -}}
{{- if .Values.profiling.enabled -}}
true
{{- end -}}
{{- end -}}

{{/*
  Effective profiles list. Autopilot adds "insights" when profiles is
  empty; a non-empty list that omits "insights" is rejected by
  odigos.validateAutopilotDeps.
*/}}
{{- define "odigos.profiles" -}}
{{- $profiles := .Values.profiles | default list -}}
{{- if and $profiles (not (kindIs "slice" $profiles)) -}}
{{- fail "Invalid 'profiles' value: must be a list. Example (YAML): profiles: [PROFILE_NAME]  |  CLI: --set profiles={PROFILE_NAME}" -}}
{{- end -}}
{{- if and (include "odigos.autopilotEnabled" .) (not (has "insights" $profiles)) -}}
{{- $profiles = prepend $profiles "insights" -}}
{{- end -}}
{{- if and (eq (toString .Values.instrumentationVersion) "v2") (not (has "instrumentation-v2" $profiles)) -}}
{{- $profiles = append $profiles "instrumentation-v2" -}}
{{- end -}}
{{- if $profiles -}}
{{- toYaml $profiles -}}
{{- end -}}
{{- end -}}

{{/*
  Fail install/upgrade when autopilot is on and insights is not enabled,
  or when profiles explicitly omits "insights". Unset profiles get
  "insights" automatically (see odigos.profiles).
*/}}
{{- define "odigos.validateAutopilotDeps" -}}
{{- if include "odigos.autopilotEnabled" . }}
{{- if not .Values.insights.enabled -}}
{{- fail "insights.autopilot.enabled=true requires insights, but insights.enabled is not true. Set insights.enabled=true, or disable autopilot." -}}
{{- end -}}
{{- $profiles := .Values.profiles | default list -}}
{{- if and $profiles (not (kindIs "slice" $profiles)) -}}
{{- fail "Invalid 'profiles' value: must be a list. Example (YAML): profiles: [PROFILE_NAME]  |  CLI: --set profiles={PROFILE_NAME}" -}}
{{- end -}}
{{- if and $profiles (not (has "insights" $profiles)) -}}
{{- fail (printf "insights.autopilot.enabled=true requires the insights profile. profiles is set to %v, which does not include \"insights\". Omit profiles (defaults to [insights]) or add \"insights\" to the list." $profiles) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
  Bundled LiteLLM ships with autopilot (in-cluster only; worker URL is fixed).
*/}}
{{- define "odigos.litellmEnabled" -}}
{{- include "odigos.autopilotEnabled" . -}}
{{- end -}}

{{/*
  LiteLLM upstream model provider (e.g. openai, gemini).
*/}}
{{- define "odigos.autopilotModelProvider" -}}
{{- .Values.insights.autopilot.model.provider | default "openai" -}}
{{- end -}}

{{/*
  LiteLLM upstream model name (e.g. gpt-4o-mini).
*/}}
{{- define "odigos.autopilotModelName" -}}
{{- .Values.insights.autopilot.model.name | default "gpt-4o-mini" -}}
{{- end -}}

{{/*
  LiteLLM upstream model id: <provider>/<name> (e.g. openai/gpt-4o-mini).
*/}}
{{- define "odigos.autopilotUpstreamModel" -}}
{{- printf "%s/%s" (include "odigos.autopilotModelProvider" .) (include "odigos.autopilotModelName" .) -}}
{{- end -}}

{{/*
  Optional LiteLLM api_base for BYO inference (model.apiBase).
*/}}
{{- define "odigos.autopilotApiBase" -}}
{{- if .Values.insights.autopilot.model.apiBase -}}
{{- .Values.insights.autopilot.model.apiBase | trimSuffix "/" -}}
{{- end -}}
{{- end -}}


{{/*
  Return cleaned Kubernetes version, keeping leading 'v', removing vendor suffix like -eks-...
  */}}
  {{- define "utils.cleanKubeVersion" -}}
  {{- regexReplaceAll "-.*" .Capabilities.KubeVersion.Version "" -}}
  {{- end }}

{{- define "odigos.odiglet.resolvedResources" -}}
{{- $defaults := dict "cpu" "500m" "memory" "512Mi" -}}
{{- $resources := deepCopy (.Values.odiglet.resources | default dict) -}}

{{- $requests := get $resources "requests" | default dict -}}
{{- $limits := get $resources "limits" | default dict -}}

{{- if and (empty $limits) (not (empty $requests)) -}}
  {{- $_ := set $resources "limits" $requests -}}

{{- else if and (empty $requests) (not (empty $limits)) -}}
  {{- $_ := set $resources "requests" $limits -}}

{{- else if and (empty $limits) (empty $requests) -}}
  {{- $sizingYaml := include "odigos.odiglet.sizing.resources" . -}}
  {{- $sizing := $sizingYaml | fromYaml -}}
  {{- if $sizing }}
    {{- $_ := set $resources "limits" $sizing -}}
    {{- $_ := set $resources "requests" $sizing -}}
  {{- else }}
    {{- $_ := set $resources "limits" $defaults -}}
    {{- $_ := set $resources "requests" $defaults -}}
  {{- end }}
{{- end }}

{{- toYaml $resources -}}
{{- end }}


{{- define "odigos.gomemlimitFromResources" -}}
{{- $resources := .Resources | default dict -}}
{{- $limits := get $resources "limits" -}}
{{- $requests := get $resources "requests" -}}

{{- $raw := (get $limits "memory") | default (get $requests "memory") -}}
{{- $number := regexFind "^[0-9]+" $raw -}}
{{- $unit := regexFind "[a-zA-Z]+$" $raw -}}

{{- if and $number $unit }}
  {{- $num := int $number -}}
  {{- if eq $unit "Ki" -}}
    {{- $num = div $num 1024 -}}
  {{- else if eq $unit "Gi" -}}
    {{- $num = mul $num 1024 -}}
  {{- else if eq $unit "Ti" -}}
    {{- $num = mul $num 1048576 -}}
  {{- else if ne $unit "Mi" -}}
    {{- fail (printf "Unsupported memory unit %q in %q for GOMEMLIMIT: use Ki, Mi, Gi, or Ti" $unit $raw) -}}
  {{- end -}}
  {{- $val := div (mul $num 80) 100 -}}
  {{- printf "%dMiB" $val -}}
{{- else }}
   {{- fail (printf "Invalid memory limit format for GOMEMLIMIT: %q" $raw) -}}
{{- end }}
{{- end }}

{{/*
  Odiglet-specific GOMEMLIMIT: odiglet allocates significant memory for eBPF maps
  which live outside the Go heap. The Go runtime is unaware of this memory, so we
  use a lower GOMEMLIMIT percentage (default 60%) for memory limits >= 500Mi to
  leave enough headroom for eBPF allocations. Below 500Mi the standard 80% is used.
*/}}
{{- define "odigos.odiglet.gomemlimit" -}}
{{- $resources := include "odigos.odiglet.resolvedResources" . | fromYaml -}}
{{- $limits := get $resources "limits" -}}
{{- $requests := get $resources "requests" -}}

{{- $raw := (get $limits "memory") | default (get $requests "memory") -}}
{{- $number := regexFind "^[0-9]+" $raw -}}
{{- $unit := regexFind "[a-zA-Z]+$" $raw -}}

{{- if and $number $unit }}
  {{- $num := int $number -}}
  {{- if eq $unit "Ki" -}}
    {{- $num = div $num 1024 -}}
  {{- else if eq $unit "Gi" -}}
    {{- $num = mul $num 1024 -}}
  {{- else if eq $unit "Ti" -}}
    {{- $num = mul $num 1048576 -}}
  {{- else if ne $unit "Mi" -}}
    {{- fail (printf "Unsupported memory unit %q in %q for GOMEMLIMIT: use Ki, Mi, Gi, or Ti" $unit $raw) -}}
  {{- end -}}
  {{- $pct := 80 -}}
  {{- if ge $num 500 -}}
    {{- $pct = int (default 60 .Values.odiglet.odiglet.goMemLimitPercentage) -}}
  {{- end -}}
  {{- $val := div (mul $num $pct) 100 -}}
  {{- printf "%dMiB" $val -}}
{{- else }}
  {{- fail (printf "Invalid memory limit format for GOMEMLIMIT: %q" $raw) -}}
{{- end }}
{{- end }}

{{/* Returns "true" when UI requires write permissions (non-readonly UI or central backend set) */}}
{{- define "odigos.ui.requiresWritePermissions" -}}
  {{- if or (ne .Values.ui.uiMode "readonly") (ne .Values.centralProxy.centralBackendURL "") -}}
true
  {{- end -}}
{{- end -}}

{{- define "odigos.odiglet.sizing.resources" -}}
{{- $s := default "size_m" .Values.ResourceSizePreset -}}
{{- $sizes := dict
  "size_s" (dict "cpu" "150m" "memory" "300Mi")
  "size_m" (dict "cpu" "500m" "memory" "500Mi")
  "size_l" (dict "cpu" "750m" "memory" "750Mi")
  "size_xl" (dict "cpu" "1000m" "memory" "1024Mi")
-}}
{{- with (get $sizes $s) -}}
{{ toYaml . }}
{{- end -}}
{{- end }}

{{/* Comma-join pull secret names for CLI args */}}
{{- define "odigos.joinPullSecrets" -}}
{{- if .Values.imagePullSecrets -}}
{{- join "," .Values.imagePullSecrets -}}
{{- end -}}
{{- end }}

{{/* Render imagePullSecrets, including the on-prem registry pull secret when pulling from registry.odigos.io. */}}
{{- define "odigos.renderPullSecrets" -}}
{{- $enterpriseSecret := include "odigos.enterpriseRegistryPullSecretToMount" . -}}
{{- if or .Values.imagePullSecrets $enterpriseSecret }}
imagePullSecrets:
{{- range .Values.imagePullSecrets }}
  - name: {{ . | quote }}
{{- end }}
{{- if $enterpriseSecret }}
  - name: {{ $enterpriseSecret | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/* Returns true when URL templatization live traffic learning is enabled. */}}
{{- define "cardinalityControl.urlTemplatization.liveTrafficLearning.enabled" -}}
{{- and .Values.cardinalityControl .Values.cardinalityControl.urlTemplatization .Values.cardinalityControl.urlTemplatization.liveTrafficLearning .Values.cardinalityControl.urlTemplatization.liveTrafficLearning.enabled -}}
{{- end }}

{{/* Fail install/upgrade when automaticRules.enabled is set without liveTrafficLearning.enabled. */}}
{{- define "cardinalityControl.urlTemplatization.liveTrafficLearning.validate" -}}
{{- $ltl := dict -}}
{{- if and .Values.cardinalityControl .Values.cardinalityControl.urlTemplatization .Values.cardinalityControl.urlTemplatization.liveTrafficLearning -}}
{{- $ltl = .Values.cardinalityControl.urlTemplatization.liveTrafficLearning -}}
{{- end -}}
{{- $automaticRules := get $ltl "automaticRules" | default dict -}}
{{- if and (get $automaticRules "enabled") (not (get $ltl "enabled")) -}}
{{- fail "cardinalityControl.urlTemplatization.liveTrafficLearning.automaticRules.enabled requires cardinalityControl.urlTemplatization.liveTrafficLearning.enabled to be true." -}}
{{- end -}}
{{- end }}

{{/* Returns true when the shared cache DB should be deployed (any consuming feature enabled). */}}
{{- define "cacheDb.enabled" -}}
{{- $ltl := eq (include "cardinalityControl.urlTemplatization.liveTrafficLearning.enabled" .) "true" -}}
{{- $autopilot := include "odigos.autopilotEnabled" . -}}
{{- if or $ltl $autopilot -}}
true
{{- end -}}
{{- end }}

{{/*
  Feature-derived default resources for the shared cache when cacheDb.resources is unset.
  Used only by cacheDb.resolvedResources; user-configured resources always win.
  When multiple consuming features are enabled, sum each feature's defaults.
  Both features use Burstable QoS (requests below limits). Autopilot limits
  are 100m CPU / 500Mi memory.
*/}}
{{- define "cacheDb.featureDefaultResources" -}}
{{- $ltl := eq (include "cardinalityControl.urlTemplatization.liveTrafficLearning.enabled" .) "true" -}}
{{- $autopilot := include "odigos.autopilotEnabled" . -}}
{{- if and $ltl $autopilot -}}
requests:
  cpu: 75m
  memory: 192Mi
limits:
  cpu: 300m
  memory: 756Mi
{{- else if $autopilot -}}
requests:
  cpu: 25m
  memory: 128Mi
limits:
  cpu: 100m
  memory: 500Mi
{{- else if $ltl -}}
requests:
  cpu: 50m
  memory: 64Mi
limits:
  cpu: 200m
  memory: 256Mi
{{- end -}}
{{- end }}

{{/* Effective cache resources: user override, else feature-derived defaults. */}}
{{- define "cacheDb.resolvedResources" -}}
{{- $cacheDb := .Values.cacheDb | default dict -}}
{{- $resources := deepCopy (get $cacheDb "resources" | default dict) -}}
{{- $requests := get $resources "requests" | default dict -}}
{{- $limits := get $resources "limits" | default dict -}}
{{- if and (empty $limits) (not (empty $requests)) -}}
  {{- $_ := set $resources "limits" $requests -}}
{{- else if and (empty $requests) (not (empty $limits)) -}}
  {{- $_ := set $resources "requests" $limits -}}
{{- else if and (empty $limits) (empty $requests) -}}
  {{- $resources = include "cacheDb.featureDefaultResources" . | fromYaml -}}
{{- end -}}
{{- toYaml $resources -}}
{{- end }}

{{/*
  Soft memory cap for the cache process. User override, else ~85% of the
  effective memory limit (Mi → mb) to leave process overhead headroom.
*/}}
{{- define "cacheDb.resolvedMaxmemory" -}}
{{- $cacheDb := .Values.cacheDb | default dict -}}
{{- if get $cacheDb "maxmemory" -}}
{{- get $cacheDb "maxmemory" -}}
{{- else -}}
{{- $resources := include "cacheDb.resolvedResources" . | fromYaml -}}
{{- $raw := (get (get $resources "limits" | default dict) "memory") | default (get (get $resources "requests" | default dict) "memory") -}}
{{- $number := regexFind "^[0-9]+" ($raw | toString) -}}
{{- $unit := regexFind "[a-zA-Z]+$" ($raw | toString) -}}
{{- $num := int $number -}}
{{- if eq $unit "Ki" -}}
  {{- $num = div $num 1024 -}}
{{- else if eq $unit "Gi" -}}
  {{- $num = mul $num 1024 -}}
{{- else if eq $unit "Ti" -}}
  {{- $num = mul $num 1048576 -}}
{{- else if and (ne $unit "Mi") (ne $unit "") -}}
  {{- fail (printf "Unsupported memory unit %q in cacheDb resources for maxmemory derivation" $unit) -}}
{{- end -}}
{{- printf "%dmb" (div (mul $num 85) 100) -}}
{{- end -}}
{{- end }}
