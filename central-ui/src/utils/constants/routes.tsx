export const ROUTES = {
  ROOT: '/',
  SIGN_IN: '/signin',
  UPDATE_YOUR_PASSWORD: '/update-password',
  ACCESS_MANAGEMENT: '/access-management',
  CONNECTIONS: '/connections',
  OVERVIEW: '/overview',
  SOURCES: '/sources',
  DESTINATIONS: '/destinations',
  ACTIONS: '/actions',
  INSTRUMENTATION_RULES: '/instrumentation-rules',
  SERVICE_MAP: '/service-map',
  PIPELINE_COLLECTORS: '/pipeline-collectors',
};

const PROTOCOL = typeof window !== 'undefined' ? window.location.protocol : 'http';
const HOSTNAME = typeof window !== 'undefined' ? window.location.hostname : '';
const IS_INGRESSED_DOMAIN = HOSTNAME && HOSTNAME !== 'localhost';

const BACKEND_PORT = 8081;
const BACKEND_WS_ORIGIN = IS_INGRESSED_DOMAIN ? `ws://${HOSTNAME}` : `ws://localhost:${BACKEND_PORT}`;
const BACKEND_HTTP_ORIGIN = IS_INGRESSED_DOMAIN ? `${PROTOCOL}//${HOSTNAME}` : `http://localhost:${BACKEND_PORT}`;

export const API = {
  GRAPHQL_WS: `${BACKEND_WS_ORIGIN}/graphql`,
  GRAPHQL_HTTP: `${BACKEND_HTTP_ORIGIN}/graphql`,
  SSE_EVENTS: `${BACKEND_HTTP_ORIGIN}/sse/events`,
};
