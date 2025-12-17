export const STORAGE_KEYS = {
  AUTH_STORAGE: 'odigos-auth-storage',
  SELECTED_PROXY: 'SELECTED_PROXY',
  SELECTED_DATA_STREAM: (proxyID?: string) => `SELECTED_DATA_STREAM_${proxyID}`,
};
