// Remote configuration managed by central-backend
// These settings take precedence over Helm values and persist through upgrades

interface RolloutInput {
  automaticRolloutDisabled?: boolean;
}

export interface RemoteConfigInput {
  rollout?: RolloutInput;
}
