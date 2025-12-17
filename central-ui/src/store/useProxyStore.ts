import { STORAGE_KEYS } from '@/utils';
import { create } from 'zustand';
import { persist } from 'zustand/middleware';

interface ProxyStoreState {
  proxyID?: string;
  odigosVersion?: string;
}

interface ProxyStoreActions {
  setProxyID: (proxyID: ProxyStoreState['proxyID']) => void;
  setOdigosVersion: (odigosVersion: ProxyStoreState['odigosVersion']) => void;

  setProxyStore: (payload: ProxyStoreState) => void;
  resetProxyStore: () => void;
}

const initialState: ProxyStoreState = {
  proxyID: undefined,
  odigosVersion: undefined,
};

export const useProxyStore = create<ProxyStoreState & ProxyStoreActions>()(
  persist(
    (set) => ({
      ...initialState,

      setProxyID: (proxyID) => set({ proxyID }),
      setOdigosVersion: (odigosVersion) => set({ odigosVersion }),

      setProxyStore: (payload) => set(payload),
      resetProxyStore: () => set(initialState),
    }),
    {
      name: STORAGE_KEYS.SELECTED_PROXY,
      partialize: (state) => ({
        proxyID: state.proxyID,
        odigosVersion: state.odigosVersion,
      }),
    },
  ),
);
