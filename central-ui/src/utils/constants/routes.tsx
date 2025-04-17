export const ROUTES = {
  ROOT: '/',
  CONNECTIONS: '/connections',

  OVERVIEW: (proxyID: string) => `/overview/${proxyID}`,
  SOURCES: (proxyID: string) => `/sources/${proxyID}`,
  DESTINATIONS: (proxyID: string) => `/destinations/${proxyID}`,
  ACTIONS: (proxyID: string) => `/actions/${proxyID}`,
  INSTRUMENTATION_RULES: (proxyID: string) => `/instrumentation-rules/${proxyID}`,
};
