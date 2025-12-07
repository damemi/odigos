export const UPDATE_REMOTE_CONFIG = `
  mutation UpdateRemoteConfig($config: RemoteConfigInput!) {
    updateRemoteConfig(config: $config) {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`;

