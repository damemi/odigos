import { create } from 'zustand';
import { User, Role } from '@/types/auth';
import { persist } from 'zustand/middleware';
import { OidcProviderId } from '@/types';

interface UserStoreState {
  authProviderId: OidcProviderId | null;

  idToken: string | null; // for logout
  accessToken: string | null; // for backend auth
  user: User | null;

  lastAuthEmail: string | null;
  isAdmin: boolean;
}

interface UserStoreActions {
  setAuthProviderId: (providerId: UserStoreState['authProviderId']) => void;
  setAuth: (payload: { idToken: UserStoreState['idToken']; accessToken: UserStoreState['accessToken']; user: UserStoreState['user'] }) => void;
  clearAuth: () => void;
  updateUser: (payload: Partial<User>) => void;
  setLastAuthEmail: (email: string | null) => void;
}

export const useUserStore = create<UserStoreState & UserStoreActions>()(
  persist(
    (set, get) => ({
      authProviderId: null,
      idToken: null,
      accessToken: null,
      user: null,
      isAdmin: false,
      lastAuthEmail: null,

      setAuthProviderId: (providerId) =>
        set({
          authProviderId: providerId,
        }),

      setAuth: ({ idToken, accessToken, user }) =>
        set({
          idToken,
          accessToken,
          user,
          isAdmin: user?.role && [Role.ADMIN, Role.OWNER].includes(user.role),
        }),

      clearAuth: () =>
        set({
          authProviderId: null,
          idToken: null,
          accessToken: null,
          user: null,
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
      name: 'odigos-auth-storage',
      partialize: (state) => ({
        authProviderId: state.authProviderId,
        idToken: state.idToken,
        accessToken: state.accessToken,
        user: state.user,
        isAdmin: state.isAdmin,
        lastAuthEmail: state.lastAuthEmail,
      }),
    },
  ),
);
