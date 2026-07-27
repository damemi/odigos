import { useState, useCallback } from 'react';
import { useUserStore } from '@/store';
import * as STRINGS from '@/utils/constants/strings';
import { AuthProviderId, SignInPayload, SignInStatus, SystemConfig, User } from '@/types';
import { useLazyQuery, useMutation, useQuery } from '@apollo/client/react';
import { SIGN_IN, SET_INITIAL_PASSWORD, GET_SYSTEM_CONFIG, GET_SIGN_OUT_URL } from '@/graphql';

export type HandleSignIn = (credentials: { email?: string; password?: string; code?: string }) => void;
export type HandleSetInitialPassword = (email: string, password: string) => void;
export type HandleSignOut = () => Promise<void>;
export type GetCurrentUser = () => User | null;
/** Returns true if the browser is navigating away for IdP logout. */
export type EnsureIdpLogoutBeforeSso = (providerId: AuthProviderId) => Promise<boolean>;

const stripAuthCodeFromUrl = () => {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  url.searchParams.delete(STRINGS.AUTH_CODE_QUERY_PARAM);
  url.searchParams.delete('session_state');
  url.searchParams.delete('iss');
  const next = `${url.pathname}${url.search}${url.hash}`;
  window.history.replaceState(null, '', next);
};

export const useAuth = () => {
  const [error, setError] = useState<string | null>(null);
  const { setAuth, clearAuth, setLastAuthEmail } = useUserStore();

  const { data: systemConfigData, loading: systemConfigLoading } = useQuery<{ systemConfig: SystemConfig }>(GET_SYSTEM_CONFIG);

  const [signIn, { loading: signInLoading }] = useMutation<{ signIn: SignInPayload }>(SIGN_IN);
  const [getSignOutUrl, { loading: signOutLoading }] = useLazyQuery<{ signOutUrl: string | null }, { idToken: string | null }>(GET_SIGN_OUT_URL);
  const [setInitialPassword, { loading: passwordUpdateLoading }] = useMutation<{ setInitialPassword: SignInPayload }>(SET_INITIAL_PASSWORD);

  const clearError = useCallback(() => {
    setError(null);
  }, []);

  const handleSignIn: HandleSignIn = async (credentials) => {
    setError(null);
    try {
      if (credentials.email) setLastAuthEmail(credentials.email);
      const { data } = await signIn({ variables: credentials });
      if (!data?.signIn) return;

      const { status, idToken, accessToken, user } = data.signIn;
      if (status === SignInStatus.ACCESS_DENIED) {
        clearAuth();
        stripAuthCodeFromUrl();
        // Keep idToken only for a deferred Keycloak/IdP logout on the next SSO click —
        // do not redirect now so the access-denied message is shown immediately.
        if (idToken) {
          sessionStorage.setItem(STRINGS.PENDING_IDP_LOGOUT_TOKEN_KEY, idToken);
        }
        setError(STRINGS.ACCESS_DENIED_ERROR);
        return;
      }

      sessionStorage.removeItem(STRINGS.PENDING_IDP_LOGOUT_TOKEN_KEY);
      sessionStorage.removeItem(STRINGS.AUTO_SSO_PROVIDER_KEY);

      // PASSWORD_UPDATE_REQUIRED intentionally has no user accessToken (null).
      // Keep lastAuthEmail so /update-password can call setInitialPassword.
      setAuth({
        idToken: idToken || null,
        accessToken: accessToken || null,
        user: user || null,
        status,
      });
      // Central UI redirect is handled in protected-route.tsx
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      console.error(err);
    }
  };

  // After ACCESS_DENIED, Azure/Okta browser SSO can silently re-auth the same user.
  // On the next SSO click, log out of Keycloak (propagates to IdP via id_token_hint),
  // then auto-resume that provider when we land back on /signin.
  const ensureIdpLogoutBeforeSso: EnsureIdpLogoutBeforeSso = async (providerId) => {
    const pendingToken = sessionStorage.getItem(STRINGS.PENDING_IDP_LOGOUT_TOKEN_KEY);
    if (!pendingToken) return false;

    sessionStorage.removeItem(STRINGS.PENDING_IDP_LOGOUT_TOKEN_KEY);
    sessionStorage.setItem(STRINGS.AUTO_SSO_PROVIDER_KEY, providerId);

    try {
      const { data } = await getSignOutUrl({ variables: { idToken: pendingToken } });
      const url = data?.signOutUrl;
      if (url) {
        window.location.href = url;
        return true;
      }
    } catch (err) {
      console.error(err);
      sessionStorage.removeItem(STRINGS.AUTO_SSO_PROVIDER_KEY);
    }
    return false;
  };

  const consumeAutoSsoProvider = useCallback((): AuthProviderId | null => {
    const raw = sessionStorage.getItem(STRINGS.AUTO_SSO_PROVIDER_KEY);
    if (!raw) return null;
    sessionStorage.removeItem(STRINGS.AUTO_SSO_PROVIDER_KEY);
    if (Object.values(AuthProviderId).includes(raw as AuthProviderId)) {
      return raw as AuthProviderId;
    }
    return null;
  }, []);

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
    clearError,
    ensureIdpLogoutBeforeSso,
    consumeAutoSsoProvider,
    systemConfigLoading,
    systemConfig: systemConfigData?.systemConfig,
    loading: signInLoading || passwordUpdateLoading || signOutLoading,
    error,
  };
};
