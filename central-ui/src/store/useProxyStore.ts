import { create } from 'zustand';
import { STORAGE_KEYS } from '@/utils';
import { PlatformType } from '@odigos/ui-kit/types';
import { createJSONStorage, persist } from 'zustand/middleware';

interface ProxyStoreState {
  proxyID?: string;
  proxyName?: string;
  odigosVersion?: string;
  // agents-api GraphQL schema version (VM proxies only); drives VM dialect selection.
  graphqlSchemaVersion?: string;
  platformType?: PlatformType;
}

interface ProxyStoreActions {
  setProxyID: (proxyID: ProxyStoreState['proxyID']) => void;
  setProxyName: (proxyName: ProxyStoreState['proxyName']) => void;
  setOdigosVersion: (odigosVersion: ProxyStoreState['odigosVersion']) => void;
  setGraphqlSchemaVersion: (graphqlSchemaVersion: ProxyStoreState['graphqlSchemaVersion']) => void;
  setPlatformType: (platformType: ProxyStoreState['platformType']) => void;

  setProxyStore: (payload: ProxyStoreState) => void;
  resetProxyStore: () => void;
}

const initialState: ProxyStoreState = {
  proxyID: undefined,
  proxyName: undefined,
  odigosVersion: undefined,
  graphqlSchemaVersion: undefined,
  platformType: undefined,
};

export const useProxyStore = create<ProxyStoreState & ProxyStoreActions>()(
  persist(
    (set) => ({
      ...initialState,

      setProxyID: (proxyID) => set({ proxyID }),
      setProxyName: (proxyName) => set({ proxyName }),
      setOdigosVersion: (odigosVersion) => set({ odigosVersion }),
      setGraphqlSchemaVersion: (graphqlSchemaVersion) => set({ graphqlSchemaVersion }),
      setPlatformType: (platformType) => set({ platformType }),

      setProxyStore: (payload) => set(payload),
      resetProxyStore: () => set(initialState),
    }),
    {
      name: STORAGE_KEYS.SELECTED_PROXY,
      storage: createJSONStorage(() => sessionStorage),
      partialize: (state) => ({
        proxyID: state.proxyID,
        proxyName: state.proxyName,
        odigosVersion: state.odigosVersion,
        graphqlSchemaVersion: state.graphqlSchemaVersion,
        platformType: state.platformType,
      }),
    },
  ),
);
