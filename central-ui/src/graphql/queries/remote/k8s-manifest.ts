import { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_K8S_MANIFEST: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // [T-MOBILE-ALIGNMENT] Keyed 'v1.11', not 'v1.20'. `k8sManifest(namespace, kind, name)`
    // has existed unchanged since v1.11.0 — this exact document validates against all 109
    // GA schemas v1.11.0..v1.32.1 (scratchpad/find-bucket-floor.js: valid everywhere,
    // contiguous, every patch passes), and a v1.17.13 agent serves all three kinds the
    // Debug tab asks for (Deployment 2271 chars, InstrumentationConfig 2075, Pod 3384).
    //
    // The v1.20 floor made `canRun` false below it, and runner.ts returns a SOFT
    // `{data: undefined}` with no error for a gated op — so all three YAML cards rendered
    // "YAML not found" with no error toast and no upgrade hint, on an agent that would
    // have answered every one of them.
    //
    // Like the workload bucket, this is a permanent correctness fix and not scaffolding:
    // reverting it re-blanks the Debug tab for every agent below v1.20.
    'v1.11': `
  query GetK8sManifest($namespace: String!, $kind: K8sResourceKind!, $name: String!) {
    k8sManifest(namespace: $namespace, kind: $kind, name: $name)
  }`,
  },
};
