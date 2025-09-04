export const ROUTES = {
  ROOT: '/',
  SIGN_IN: '/signin',
  CONNECTIONS: '/connections',
  UPDATE_YOUR_PASSWORD: '/update-password',
  ACCESS_MANAGEMENT: '/access-management',

  OVERVIEW: (proxyID: string) => `/overview/${proxyID}`,
  SOURCES: (proxyID: string) => `/sources/${proxyID}`,
  DESTINATIONS: (proxyID: string) => `/destinations/${proxyID}`,
  ACTIONS: (proxyID: string) => `/actions/${proxyID}`,
  INSTRUMENTATION_RULES: (proxyID: string) => `/instrumentation-rules/${proxyID}`,

  SERVICE_MAP: (proxyID: string) => `/service-map/${proxyID}`,
};

const IS_PROD = process.env.NODE_ENV === 'production';
const PORT = process.env.PORT || 8081;

const DEV_HTTP_URL = `http://localhost:${PORT}`;
const DEV_WS_URL = `ws://localhost:${PORT}`;
const PROD_HTTP_ORIGIN = typeof window !== 'undefined' ? `http://${window.location.hostname}:${PORT}` : DEV_HTTP_URL;
const PROD_WS_ORIGIN = typeof window !== 'undefined' ? 'ws://' + window.location.hostname + PORT : DEV_WS_URL;

export const API = {
  GRAPHQL_HTTP: `${IS_PROD ? PROD_HTTP_ORIGIN : DEV_HTTP_URL}/graphql`,
  GRAPHQL_WS: `${IS_PROD ? PROD_WS_ORIGIN : DEV_WS_URL}/graphql`,
  SSE_EVENTS: `${DEV_HTTP_URL}/sse/events`,
};
