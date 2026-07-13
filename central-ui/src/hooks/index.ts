// Most CRUD/data hooks have been removed in favor of `useOdigosApi()` from
// `@odigos/ui-kit/contexts`. The hooks that remain in central-ui
// are host-only concerns:
//   - auth (`useAuth`, `useAuthProvider`) — sign-in flow + page redirects.
//   - common — `useSSE` (long-lived EventSource listener), `useHealth`
//     (status polling).
//   - compute-platforms — `useComputePlatformsCRUD` is direct Apollo on
//     the central backend; `useProxy` reads from the proxy store and
//     orchestrates routing/store cleanup.
//   - config — `useConfig` returns readonly state derived from user role.
//   - proxy — `useProxy`, `useProxyCRUD` (proxy selection + delete-proxy
//     mutation; not per-proxy CRUD).
//   - teams / users — admin-only host concerns; not v2 container props.
//   - tokens — `useTokenTracker` only (`useTokenCRUD` deleted; the kit's
//     API context owns token CRUD).
export * from './auth';
export * from './common';
export * from './compute-platforms';
export * from './config';
export * from './proxy';
export * from './role-mappings';
export * from './teams';
export * from './tokens';
export * from './users';
