import { useSearchParams } from 'next/navigation';
import { useCallback, useEffect } from 'react';
import { useAuth } from './useAuth';
import { useLazyQuery, useMutation } from '@apollo/client';
import { ENSURE_IDENTITY_PROVIDER, GET_OIDC_SIGN_IN_URL } from '@/graphql';
import type { GeneralResult, OidcProviderId, OidcProviderInput } from '@/types';

interface GetOidcSignInUrlRequest {
  providerId: OidcProviderId;
}
interface GetOidcSignInUrlResult {
  oidcSignInUrl: string;
}

interface EnsureIdentityProviderRequest {
  input: OidcProviderInput;
}
interface EnsureIdentityProviderResult {
  ensureIdentityProvider: GeneralResult;
}

export type GetOidcSignInUrl = (providerId: OidcProviderId) => Promise<string | undefined>;
export type EnsureIdentityProvider = (input: OidcProviderInput) => Promise<GeneralResult | undefined>;

export const useOidcProvider = () => {
  const { getCurrentUser, signIn } = useAuth();
  const searchParams = useSearchParams();
  const oidcCode = searchParams.get('code');

  useEffect(() => {
    if (oidcCode) {
      const signedIn = getCurrentUser();
      if (!signedIn) signIn({ code: oidcCode });
    }
  }, [oidcCode]);

  const [getUrl, { loading: getUrlLoading, error: getUrlError }] = useLazyQuery<GetOidcSignInUrlResult, GetOidcSignInUrlRequest>(GET_OIDC_SIGN_IN_URL, {
    onError: (error) => {
      console.error(error);
    },
  });
  const [ensureIdp, { loading: ensureIdpLoading, error: ensureIdpError }] = useMutation<EnsureIdentityProviderResult, EnsureIdentityProviderRequest>(ENSURE_IDENTITY_PROVIDER, {
    onError: (error) => {
      console.error(error);
    },
  });

  const getOidcSignInUrl: GetOidcSignInUrl = useCallback(async (providerId) => {
    const { data } = await getUrl({ variables: { providerId } });
    return data?.oidcSignInUrl;
  }, []);

  const ensureIdentityProvider: EnsureIdentityProvider = async (input) => {
    const { data } = await ensureIdp({ variables: { input } });
    return data?.ensureIdentityProvider;
  };

  return {
    loading: getUrlLoading || ensureIdpLoading,
    error: (getUrlError || ensureIdpError)?.message,
    getOidcSignInUrl,
    ensureIdentityProvider,
  };
};
