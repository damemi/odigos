import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { User } from '@/types/auth';

interface UserStoreState {
  user: User | null;
  token: string | null;
  isAuthenticated: boolean;
}

interface UserStoreActions {
  setUser: (user: User | null) => void;
  setToken: (token: string | null) => void;
  setAuth: (user: User, token: string) => void;
  clearAuth: () => void;
  updateUser: (updates: Partial<User>) => void;
}

export const useUserStore = create<UserStoreState & UserStoreActions>()(
  persist(
    (set, get) => ({
      user: null,
      token: null,
      isAuthenticated: false,

      setUser: (user) =>
        set({
          user,
          isAuthenticated: !!user,
        }),

      setToken: (token) =>
        set({
          token,
        }),

      setAuth: (user, token) =>
        set({
          user,
          token,
          isAuthenticated: true,
        }),

      clearAuth: () =>
        set({
          user: null,
          token: null,
          isAuthenticated: false,
        }),

      updateUser: (updates) => {
        const currentUser = get().user;
        if (currentUser) {
          set({
            user: { ...currentUser, ...updates },
          });
        }
      },
    }),
    {
      name: 'user-storage',
      partialize: (state) => ({
        user: state.user,
        token: state.token,
      }),
    },
  ),
);
