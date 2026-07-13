import { useState, useCallback } from 'react';
import { useUserStore } from '@/store';
import { SignInPayload, SignInStatus, SystemConfig, User } from '@/types';
import { useLazyQuery, useMutation, useQuery } from '@apollo/client/react';
import { SIGN_IN, SET_INITIAL_PASSWORD, GET_SYSTEM_CONFIG, GET_SIGN_OUT_URL } from '@/graphql';

export type HandleSignIn = (credentials: { email?: string; password?: string; code?: string }) => void;
export type HandleSetInitialPassword = (email: string, password: string) => void;
export type HandleSignOut = () => Promise<void>;
export type GetCurrentUser = () => User | null;

export const useAuth = () => {
  const [error, setError] = useState<string | null>(null);
  const { setAuth, clearAuth, setLastAuthEmail } = useUserStore();

  const { data: systemConfigData, loading: systemConfigLoading } = useQuery<{ systemConfig: SystemConfig }>(GET_SYSTEM_CONFIG);

  const [signIn, { loading: signInLoading }] = useMutation<{ signIn: SignInPayload }>(SIGN_IN);
  const [getSignOutUrl, { loading: signOutLoading }] = useLazyQuery<{ signOutUrl: string | null }, { idToken: string | null }>(GET_SIGN_OUT_URL);
  const [setInitialPassword, { loading: passwordUpdateLoading }] = useMutation<{ setInitialPassword: SignInPayload }>(SET_INITIAL_PASSWORD);

  const handleSignIn: HandleSignIn = async (credentials) => {
    setError(null);
    try {
      if (credentials.email) setLastAuthEmail(credentials.email);
      const { data } = await signIn({ variables: credentials });
      if (!data?.signIn) return;

      const { status, idToken, accessToken, user } = data.signIn;
      setAuth({
        idToken: idToken || '',
        accessToken: accessToken || '',
        user: user || null,
        status,
      });
      // Central UI redirect is handled in protected-route.tsx
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      console.error(err);
    }
  };

  const handleSignOut: HandleSignOut = async () => {
    setError(null);
    try {
      const { idToken } = useUserStore.getState();
      const { data } = await getSignOutUrl({ variables: { idToken } });
      const url = data?.signOutUrl;

      // Clear local auth state first
      clearAuth();

      if (url) {
        // Use window.location.href for full browser navigation to Keycloak logout endpoint
        // This ensures cookies are cleared. DO NOT use router.replace() as that's client-side only.
        window.location.href = url;
      }
      // else Central UI redirect is handled in protected-route.tsx
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      console.error(err);
    }
  };

  const getCurrentUser: GetCurrentUser = useCallback(() => {
    const { user } = useUserStore.getState();
    return user;
  }, []);

  const handleSetInitialPassword: HandleSetInitialPassword = async (email, password) => {
    setError(null);
    try {
      const { data } = await setInitialPassword({ variables: { email, password } });
      if (!data?.setInitialPassword) return;

      const { idToken, accessToken, user } = data.setInitialPassword;
      setLastAuthEmail(null);
      setAuth({
        idToken: idToken || '',
        accessToken: accessToken || '',
        user: user || null,
        status: SignInStatus.SUCCESS,
      });
      // Central UI redirect is handled in protected-route.tsx
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      console.error(err);
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
