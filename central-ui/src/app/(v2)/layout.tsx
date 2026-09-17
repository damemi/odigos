'use client';

import React, { type PropsWithChildren } from 'react';
import { useProxyStore } from '@/store';
import { OdigosProvider } from '@odigos/ui-kit/contexts';
import { PlatformType, Tier } from '@odigos/ui-kit/types';

// `<OdigosProvider>` lives here, above BOTH route groups, rather than in
// `(in-proxy)` alone. `useOdigos()` is a plain React context with a default of
// `{ platformType: K8s, tier: Community, version: 'v0.0.0' }` — so any page
// rendered outside a provider silently reads as COMMUNITY. `(out-proxy)` had no
// provider, which made `/connections` claim the open-source tier: the cloud
// connector "created successfully" modal rendered the community logo, and the
// bulk Add Source drawer locked StaticPod workloads behind an enterprise gate.
// Central is always enterprise, hence the hardcoded `Tier.Onprem`.
//
// Note this is a DIFFERENT context from the adapter's `OperationContext` (which
// already defaulted to `Tier.Onprem`) — fixing one does not fix the other.
//
// Read from the proxy store directly rather than `useProxy()`: that hook owns
// redirect/clear effects, and mounting another instance at a layout that never
// unmounts across group navigation would run them on every transition.
function Layout({ children }: PropsWithChildren) {
  const { platformType, odigosVersion } = useProxyStore();

  return (
    <OdigosProvider platformType={platformType ?? PlatformType.K8s} tier={Tier.Onprem} version={odigosVersion || ''}>
      {children}
    </OdigosProvider>
  );
}

export default Layout;
