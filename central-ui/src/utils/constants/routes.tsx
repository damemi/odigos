export const ROUTES = {
  ROOT: '/',
  SIGN_IN: '/signin',
  CONNECTIONS: '/connections',
  UPDATE_PASSWORD: '/update-password',
  ACCESS_MANAGEMENT: '/access-management',

  OVERVIEW: (proxyID: string) => `/overview/${proxyID}`,
  SOURCES: (proxyID: string) => `/sources/${proxyID}`,
  DESTINATIONS: (proxyID: string) => `/destinations/${proxyID}`,
  ACTIONS: (proxyID: string) => `/actions/${proxyID}`,
  INSTRUMENTATION_RULES: (proxyID: string) => `/instrumentation-rules/${proxyID}`,
};

const IS_PROD = process.env.NODE_ENV === 'production';

const DEV_HTTP_URL = 'http://localhost:8081';
const DEV_WS_URL = 'ws://localhost:8081';
const PROD_HTTP_ORIGIN = typeof window !== 'undefined' ? 'http://' + window.location.hostname + ':8081' : 'http://localhost:8081';
const PROD_WS_ORIGIN = typeof window !== 'undefined' ? 'ws://' + window.location.hostname + ':8081' : 'ws://localhost:8081';

export const API = {
  GRAPHQL_HTTP: `${IS_PROD ? PROD_HTTP_ORIGIN : DEV_HTTP_URL}/graphql`,
  GRAPHQL_WS: `${IS_PROD ? PROD_WS_ORIGIN : DEV_WS_URL}/graphql`,
};
