export const ROUTES = {
  // Out-of-proxy pages
  ROOT: '/',
  SIGN_IN: '/signin',
  UPDATE_YOUR_PASSWORD: '/update-password',
  ACCESS_MANAGEMENT: '/access-management',
  CONNECTIONS: '/connections',

  // In-proxy pages
  OVERVIEW: '/overview',
  SOURCES: '/sources',
  DESTINATIONS: '/destinations',
  ACTIONS: '/actions',
  INSTRUMENTATION_RULES: '/instrumentation-rules',
  SERVICE_MAP: '/service-map',
  PIPELINE_COLLECTORS: '/pipeline-collectors',
};

const ORIGIN = typeof window !== 'undefined' ? window.location.origin : '';
const WS_ORIGIN = typeof window !== 'undefined' ? `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}` : '';

export const API = {
  GRAPHQL_WS: `${WS_ORIGIN}/graphql`,
  GRAPHQL_HTTP: `${ORIGIN}/graphql`,
  SSE_EVENTS: `${ORIGIN}/sse/events`,
};

export const getSSEEventsUrl = (clusterID: string) => `${ORIGIN}/sse/events?clusterID=${encodeURIComponent(clusterID)}`;
