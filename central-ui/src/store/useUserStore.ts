import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { User, Role } from '@/types/auth';

interface UserStoreState {
  user: User | null;
  token: string | null;
  isAuthenticated: boolean;
  lastAuthEmail: string | null;
  isAdmin: boolean;
}

interface UserStoreActions {
  setUser: (user: User | null) => void;
  setToken: (token: string | null) => void;
  setAuth: (user: User, token: string) => void;
  clearAuth: () => void;
  updateUser: (updates: Partial<User>) => void;
  setLastAuthEmail: (email: string | null) => void;
}

export const useUserStore = create<UserStoreState & UserStoreActions>()(
  persist(
    (set, get) => ({
      user: null,
      token: null,
      isAuthenticated: false,
      lastAuthEmail: null,
      isAdmin: false,

      setUser: (user) =>
        set({
          user,
          isAuthenticated: !!user,
          isAdmin: !!user && (user.role === Role.ADMIN || user.role === Role.OWNER),
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
          isAdmin: user.role === Role.ADMIN || user.role === Role.OWNER,
        }),

      clearAuth: () =>
        set({
          user: null,
          token: null,
          isAuthenticated: false,
          isAdmin: false,
        }),

      updateUser: (updates) => {
        const currentUser = get().user;
        if (currentUser) {
          const nextUser: User = { ...currentUser, ...updates };
          const effectiveRole: Role = updates.role ?? currentUser.role;
          set({
            user: nextUser,
            isAdmin: effectiveRole === Role.ADMIN || effectiveRole === Role.OWNER,
          });
        }
      },

      setLastAuthEmail: (email) =>
        set({
          lastAuthEmail: email,
        }),
    }),
    {
      name: 'user-storage',
      partialize: (state) => ({
        user: state.user,
        token: state.token,
        lastAuthEmail: state.lastAuthEmail,
        isAdmin: state.isAdmin,
      }),
    },
  ),
);
