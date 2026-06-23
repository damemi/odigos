import { create } from 'zustand';
import { STORAGE_KEYS } from '@/utils';
import { AuthProviderId } from '@/types';
import { persist } from 'zustand/middleware';
import { User, Role, SignInStatus } from '@/types/auth';

interface UserStoreState {
  authProviderId: AuthProviderId | null;

  idToken: string | null; // for logout
  accessToken: string | null; // for backend auth
  user: User | null;
  status: SignInStatus | null;

  lastAuthEmail: string | null;
  isAdmin: boolean;
  canEdit: boolean;
}

interface UserStoreActions {
  setAuthProviderId: (providerId: UserStoreState['authProviderId']) => void;
  setAuth: (payload: Pick<UserStoreState, 'idToken' | 'accessToken' | 'user' | 'status'>) => void;
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
      status: null,
      isAdmin: false,
      canEdit: false,
      lastAuthEmail: null,

      setAuthProviderId: (providerId) =>
        set({
          authProviderId: providerId,
        }),

      setAuth: ({ idToken, accessToken, user, status }) =>
        set({
          idToken,
          accessToken,
          user,
          status,
          isAdmin: !!user?.role && [Role.ADMIN, Role.OWNER].includes(user.role),
          canEdit: !!user?.role && [Role.ADMIN, Role.OWNER, Role.MAINTAINER].includes(user.role),
        }),

      clearAuth: () =>
        set({
          authProviderId: null,
          idToken: null,
          accessToken: null,
          user: null,
          status: null,
          isAdmin: false,
          canEdit: false,
          lastAuthEmail: null,
        }),

      updateUser: (updates) => {
        const currentUser = get().user;
        if (currentUser) {
          const nextUser: User = { ...currentUser, ...updates };
          const effectiveRole: Role = updates.role ?? currentUser.role;
          set({
            user: nextUser,
            isAdmin: effectiveRole === Role.ADMIN || effectiveRole === Role.OWNER,
            canEdit: effectiveRole === Role.ADMIN || effectiveRole === Role.OWNER || effectiveRole === Role.MAINTAINER,
          });
        }
      },

      setLastAuthEmail: (email) =>
        set({
          lastAuthEmail: email,
        }),
    }),
    {
      name: STORAGE_KEYS.AUTH_STORAGE,
      partialize: (state) => ({
        authProviderId: state.authProviderId,
        idToken: state.idToken,
        accessToken: state.accessToken,
        user: state.user,
        status: state.status,
        isAdmin: state.isAdmin,
        canEdit: state.canEdit,
        lastAuthEmail: state.lastAuthEmail,
      }),
    },
  ),
);
