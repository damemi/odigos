export const GET_CONFIG = `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }
`;
