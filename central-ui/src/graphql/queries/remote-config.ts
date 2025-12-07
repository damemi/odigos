export const GET_REMOTE_CONFIG = `
  query GetRemoteConfig {
    remoteConfig {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`;

