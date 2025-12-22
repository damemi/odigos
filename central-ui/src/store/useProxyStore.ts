import { create } from 'zustand';
import { STORAGE_KEYS } from '@/utils';
import { persist } from 'zustand/middleware';
import { PlatformType } from '@odigos/ui-kit/types';

interface ProxyStoreState {
  proxyID?: string;
  proxyName?: string;
  odigosVersion?: string;
  platformType?: PlatformType;
}

interface ProxyStoreActions {
  setProxyID: (proxyID: ProxyStoreState['proxyID']) => void;
  setProxyName: (proxyName: ProxyStoreState['proxyName']) => void;
  setOdigosVersion: (odigosVersion: ProxyStoreState['odigosVersion']) => void;
  setPlatformType: (platformType: ProxyStoreState['platformType']) => void;

  setProxyStore: (payload: ProxyStoreState) => void;
  resetProxyStore: () => void;
}

const initialState: ProxyStoreState = {
  proxyID: undefined,
  proxyName: undefined,
  odigosVersion: undefined,
  platformType: undefined,
};

export const useProxyStore = create<ProxyStoreState & ProxyStoreActions>()(
  persist(
    (set) => ({
      ...initialState,

      setProxyID: (proxyID) => set({ proxyID }),
      setProxyName: (proxyName) => set({ proxyName }),
      setOdigosVersion: (odigosVersion) => set({ odigosVersion }),
      setPlatformType: (platformType) => set({ platformType }),

      setProxyStore: (payload) => set(payload),
      resetProxyStore: () => set(initialState),
    }),
    {
      name: STORAGE_KEYS.SELECTED_PROXY,
      partialize: (state) => ({
        proxyID: state.proxyID,
        proxyName: state.proxyName,
        odigosVersion: state.odigosVersion,
        platformType: state.platformType,
      }),
    },
  ),
);
