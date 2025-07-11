import { useMutation, useQuery } from '@apollo/client';
import { ROUTES } from '@/utils';
import { useUserStore } from '@/store';
import { useRouter } from 'next/navigation';
import { useState, useCallback } from 'react';
import { SignInPayload, SignInStatus, SystemConfig } from '@/types';
import { SIGN_IN, SET_INITIAL_PASSWORD, GET_SYSTEM_CONFIG } from '@/graphql';

export const useAuth = () => {
  const router = useRouter();
  const [error, setError] = useState<string | null>(null);
  const { setAuth, clearAuth } = useUserStore();

  const { data: systemConfigData, loading: systemConfigLoading } = useQuery<{ systemConfig: SystemConfig }>(GET_SYSTEM_CONFIG);

  const [signIn, { loading: signInLoading }] = useMutation<{ signIn: SignInPayload }>(SIGN_IN, {
    onCompleted: (data) => {
      const { token, status, user } = data.signIn;
      if (user) {
        setAuth(user, token);
      }

      switch (status) {
        case SignInStatus.SUCCESS:
          router.push(ROUTES.CONNECTIONS);
          break;
        case SignInStatus.PASSWORD_UPDATE_REQUIRED:
          router.push(ROUTES.UPDATE_PASSWORD);
          break;
      }
    },
    onError: (error) => {
      setError(error.message);
    },
  });

  const [setInitialPassword, { loading: passwordUpdateLoading }] = useMutation<{ setInitialPassword: SignInPayload }>(SET_INITIAL_PASSWORD, {
    onCompleted: (data) => {
      const { token, user } = data.setInitialPassword;
      if (user) {
        setAuth(user, token);
      }
      router.push(ROUTES.CONNECTIONS);
    },
    onError: (error) => {
      setError(error.message);
    },
  });

  const handleSignIn = async (email: string, password: string) => {
    setError(null);
    try {
      await signIn({ variables: { email, password } });
    } catch (_err) {
      console.error(_err);
    }
  };

  const handleSetInitialPassword = async (email: string, password: string) => {
    setError(null);
    try {
      await setInitialPassword({ variables: { email, password } });
    } catch (_err) {
      console.error(_err);
    }
  };

  const logout = useCallback(() => {
    clearAuth();
    router.push(ROUTES.SIGN_IN);
  }, [router, clearAuth]);

  const isAuthenticated = useCallback(() => {
    const { token } = useUserStore.getState();
    return !!token;
  }, []);

  const getCurrentUser = useCallback(() => {
    const { user } = useUserStore.getState();
    return user;
  }, []);

  return {
    logout,
    getCurrentUser,
    isAuthenticated,
    signIn: handleSignIn,
    setInitialPassword: handleSetInitialPassword,
    systemConfigLoading,
    systemConfig: systemConfigData?.systemConfig,
    loading: signInLoading || passwordUpdateLoading,
    error,
  };
};
