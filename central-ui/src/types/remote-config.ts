// Remote configuration managed by central-backend
// These settings take precedence over Helm values and persist through upgrades

export interface RemoteConfigRollout {
  automaticRolloutDisabled?: boolean | null;
}

export interface RemoteConfig {
  rollout?: RemoteConfigRollout | null;
}

export interface RemoteConfigInput {
  rollout?: RemoteConfigRolloutInput | null;
}

export interface RemoteConfigRolloutInput {
  automaticRolloutDisabled?: boolean | null;
}

export interface GetRemoteConfigResponse {
  remoteConfig?: RemoteConfig | null;
}

export interface UpdateRemoteConfigResponse {
  updateRemoteConfig?: RemoteConfig | null;
}
