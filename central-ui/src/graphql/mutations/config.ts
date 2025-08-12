export const UPDATE_ODIGOS_CONFIG = `
  mutation UpdateOdigosConfig($odigosConfig: OdigosConfigurationInput!) {
    updateOdigosConfig(odigosConfig: $odigosConfig)
  }
`;
