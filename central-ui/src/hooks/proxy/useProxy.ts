import { usePathname, useRouter } from 'next/navigation';
import { useEffect, useMemo, useState } from 'react';
import { ROUTES } from '@/utils';
import { useProxyStore } from '@/store';
import { useComputePlatformsCRUD } from '../compute-platforms';
import type { ComputePlatform } from '@/types';
import { PlatformType, StatusType } from '@odigos/ui-kit/types';
import { useOdigosApi } from '@odigos/ui-kit/contexts';
import { ProgressKeys, useDataStreamStore, useProgressStore, useSetupStore } from '@odigos/ui-kit/store';

interface UseProxy {
  proxyID?: string;
  proxyName?: string;
  odigosVersion?: string;
  graphqlSchemaVersion?: string;
  platformType?: PlatformType;
  selectProxy: (id: string, fallback?: Pick<ComputePlatform, 'name' | 'odigosVersion' | 'graphqlSchemaVersion' | 'type'>) => void;
  clearProxy: () => void;
}

// Module-scoped so EVERY `useProxy()` instance (page, OverviewHeader, in-proxy
// layout) honors the same grace window. `selectProxy` writes SELECTED_PROXY
// while still on /connections, then `router.push(/overview)`. Any remount of a
// `useProxy` consumer in that window used to re-run the out-of-proxy clear
// effect and wipe the selection before navigation landed (Cypress stayed on
// /connections).
let suppressOutOfProxyClearUntil = 0;

export const useProxy = (): UseProxy => {
  const router = useRouter();
  const pathname = usePathname();

  const { resetCache } = useOdigosApi();
  const { resetProgress } = useProgressStore();
  const { resetDataStreamStore } = useDataStreamStore();
  const { clearStore: resetSetupStore } = useSetupStore();
  const { proxyID: storedProxyID, proxyName, odigosVersion, graphqlSchemaVersion, platformType, setProxyStore, resetProxyStore } = useProxyStore();

  const { computePlatforms } = useComputePlatformsCRUD();

  // Zustand persist rehydrates SELECTED_PROXY from sessionStorage asynchronously.
  // Until that finishes, storedProxyID is briefly undefined on a full page load
  // of an in-proxy route — clearing here would wipe a valid selection and bounce
  // the user to /connections (and made Cypress need a /connections re-select
  // every test).
  const [proxyStoreHydrated, setProxyStoreHydrated] = useState(() => useProxyStore.persist.hasHydrated());
  useEffect(() => {
    const unsub = useProxyStore.persist.onFinishHydration(() => setProxyStoreHydrated(true));
    // Re-check after subscribing: hydration may have finished between the initial render and this effect.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setProxyStoreHydrated(useProxyStore.persist.hasHydrated());
    return unsub;
  }, []);

  // On out-of-proxy pages, treat proxyID as undefined immediately (before effects run)
  // This prevents stale proxyID from triggering fetches before the cleanup effect runs
  const isOutOfProxyPage = useMemo(() => [ROUTES.ROOT, ROUTES.CONNECTIONS, ROUTES.ACCESS_MANAGEMENT, ROUTES.SIGN_IN, ROUTES.UPDATE_YOUR_PASSWORD].includes(pathname), [pathname]);
  const proxyID = isOutOfProxyPage ? undefined : storedProxyID;

  const resetStores = (opts?: { clearApollo?: boolean }) => {
    resetSetupStore();
    // Soft-clear Apollo's normalized cache when leaving a proxy. Prefer this
    // over remounting `<OdigosApiProvider>` (key=/clientKey epoch): remounts
    // tear down ProtectedRoute + the page tree and race Cypress clicks
    // ("page updated while this command was executing") / navigation.
    // `cache.reset()` does not abort in-flight requests (avoids Apollo #42).
    if (opts?.clearApollo) void resetCache();
    resetDataStreamStore();
    resetProgress(ProgressKeys.Instrumenting);
    resetProgress(ProgressKeys.Uninstrumenting);
  };

  const selectProxy = (id: string, fallback?: Pick<ComputePlatform, 'name' | 'odigosVersion' | 'graphqlSchemaVersion' | 'type'>) => {
    // Prefer the live Apollo list; fall back to the clicked Connection so a
    // briefly-empty `computePlatforms` observer cannot silently no-op the click.
    const found = computePlatforms.find((cp) => cp.id === id);
    const platform = found ?? (fallback ? { id, ...fallback } : undefined);
    if (!platform) return;

    // Suppress out-of-proxy clears until /overview mounts — see module note.
    suppressOutOfProxyClearUntil = Date.now() + 5000;

    // Cache was already cleared when landing on /connections. Do not clear
    // again here — only reset local UI stores, then navigate.
    setProxyStore({
      proxyID: id,
      proxyName: platform.name,
      odigosVersion: platform.odigosVersion,
      graphqlSchemaVersion: platform.graphqlSchemaVersion ?? undefined,
      platformType: platform.type,
    });
    resetStores({ clearApollo: false });
    router.push(ROUTES.OVERVIEW);
  };

  const clearProxy = (opts?: { skipRedirect?: boolean }) => {
    // Only wipe Apollo when leaving a selected proxy. Avoid clearing on every
    // out-of-proxy mount when the store is already empty.
    const hadSelectedProxy = !!useProxyStore.getState().proxyID;
    resetStores({ clearApollo: hadSelectedProxy });
    resetProxyStore();
    if (!opts?.skipRedirect) router.push(ROUTES.CONNECTIONS);
  };

  // Reset proxyID if the connection is lost. Also refresh odigosVersion /
  // graphqlSchemaVersion from the live connections list — sessionStorage keeps
  // the values from selectProxy time, so an agent rebuilt/reconnected with a
  // newer agents-api schema would otherwise keep driving dialects from the
  // stale version (e.g. Destination.disabled gated on v0.23.1 after upgrade).
  useEffect(() => {
    if (!proxyID) return;
    const found = computePlatforms.find((cp) => cp.id === proxyID);
    const connectionNotFound = computePlatforms.length && !found;
    const connectionHasError = found?.status === StatusType.Error;
    if (connectionNotFound || connectionHasError) {
      clearProxy();
      return;
    }
    if (!found) return;
    const nextSchema = found.graphqlSchemaVersion ?? undefined;
    const nextVersion = found.odigosVersion;
    const nextName = found.name;
    const nextType = found.type;
    if (
      nextSchema !== graphqlSchemaVersion ||
      nextVersion !== odigosVersion ||
      nextName !== proxyName ||
      nextType !== platformType
    ) {
      setProxyStore({
        proxyID,
        proxyName: nextName,
        odigosVersion: nextVersion,
        graphqlSchemaVersion: nextSchema,
        platformType: nextType,
      });
    }
  }, [proxyID, computePlatforms, graphqlSchemaVersion, odigosVersion, proxyName, platformType]);

  // Clear the store when landing on out-of-proxy pages (cleanup after leaving
  // a proxy). Or redirect to connections when an in-proxy route has no proxy.
  //
  // Wait for Zustand persist hydration before acting on a missing proxyID —
  // otherwise a hard refresh of /overview briefly sees storedProxyID as
  // undefined and bounces to /connections.
  //
  // Do not depend on `proxyID` for the out-of-proxy clear path: `selectProxy`
  // writes the new proxy into the store while still on /connections, and the
  // computed `proxyID` stays undefined there anyway (`isOutOfProxyPage`).
  useEffect(() => {
    if (!proxyStoreHydrated) return;
    if (isOutOfProxyPage) {
      if (Date.now() < suppressOutOfProxyClearUntil) return;
      clearProxy({ skipRedirect: true });
    } else if (!proxyID) {
      clearProxy();
    }
  }, [isOutOfProxyPage, proxyStoreHydrated]);

  return {
    proxyID,
    proxyName,
    odigosVersion,
    graphqlSchemaVersion,
    platformType,
    selectProxy,
    clearProxy,
  };
};
