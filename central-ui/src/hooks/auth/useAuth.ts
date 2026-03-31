import { useState, useCallback } from 'react';
import { useUserStore } from '@/store';
import { useLazyQuery, useMutation, useQuery } from '@apollo/client';
import { SignInPayload, SignInStatus, SystemConfig, User } from '@/types';
import { SIGN_IN, SET_INITIAL_PASSWORD, GET_SYSTEM_CONFIG, GET_SIGN_OUT_URL } from '@/graphql';

export type HandleSignIn = (credentials: { email?: string; password?: string; code?: string }) => void;
export type HandleSetInitialPassword = (email: string, password: string) => void;
export type HandleSignOut = () => Promise<void>;
export type GetCurrentUser = () => User | null;

export const useAuth = () => {
  const [error, setError] = useState<string | null>(null);
  const { setAuth, clearAuth, setLastAuthEmail } = useUserStore();

  const { data: systemConfigData, loading: systemConfigLoading } = useQuery<{ systemConfig: SystemConfig }>(GET_SYSTEM_CONFIG);

  const [signIn, { loading: signInLoading }] = useMutation<{ signIn: SignInPayload }>(SIGN_IN, {
    onCompleted: (data) => {
      const { status, idToken, accessToken, user } = data.signIn;

      setAuth({
        idToken: idToken || '',
        accessToken: accessToken || '',
        user: user || null,
        status,
      });

      // Central UI redirect is handled in protected-route.tsx
    },
    onError: (error) => {
      setError(error.message);
    },
  });

  const [getSignOutUrl, { loading: signOutLoading }] = useLazyQuery<{ signOutUrl: string | null }, { idToken: string | null }>(GET_SIGN_OUT_URL, {
    onCompleted: (data) => {
      const url = data.signOutUrl;

      // Clear local auth state first
      clearAuth();

      if (url) {
        // Use window.location.href for full browser navigation to Keycloak logout endpoint
        // This ensures cookies are cleared. DO NOT use router.replace() as that's client-side only.
        window.location.href = url;
      } else {
        // Central UI redirect is handled in protected-route.tsx
      }
    },
    onError: (error) => {
      setError(error.message);
    },
  });

  const [setInitialPassword, { loading: passwordUpdateLoading }] = useMutation<{ setInitialPassword: SignInPayload }>(SET_INITIAL_PASSWORD, {
    onCompleted: (data) => {
      const { idToken, accessToken, user } = data.setInitialPassword;

      setLastAuthEmail(null);
      setAuth({
        idToken: idToken || '',
        accessToken: accessToken || '',
        user: user || null,
        status: SignInStatus.SUCCESS,
      });

      // Central UI redirect is handled in protected-route.tsx
    },
    onError: (error) => {
      setError(error.message);
    },
  });

  const handleSignIn: HandleSignIn = async (credentials) => {
    setError(null);
    try {
      if (credentials.email) setLastAuthEmail(credentials.email);
      await signIn({ variables: credentials });
    } catch (_err) {
      console.error(_err);
    }
  };

  const handleSignOut: HandleSignOut = async () => {
    setError(null);
    try {
      const { idToken } = useUserStore.getState();
      await getSignOutUrl({ variables: { idToken } });
    } catch (_err) {
      console.error(_err);
    }
  };

  const getCurrentUser: GetCurrentUser = useCallback(() => {
    const { user } = useUserStore.getState();
    return user;
  }, []);

  const handleSetInitialPassword: HandleSetInitialPassword = async (email, password) => {
    setError(null);
    try {
      await setInitialPassword({ variables: { email, password } });
    } catch (_err) {
      console.error(_err);
    }
  };

  return {
    signIn: handleSignIn,
    signOut: handleSignOut,
    getCurrentUser,
    setInitialPassword: handleSetInitialPassword,
    systemConfigLoading,
    systemConfig: systemConfigData?.systemConfig,
    loading: signInLoading || passwordUpdateLoading || signOutLoading,
    error,
  };
};
