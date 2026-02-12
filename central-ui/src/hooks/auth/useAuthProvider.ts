import { useCallback } from 'react';
import { useLazyQuery, useMutation } from '@apollo/client';
import { ENSURE_IDENTITY_PROVIDER, ENSURE_SAML_IDENTITY_PROVIDER, SIGN_IN_URL,  } from '@/graphql';
import type { IdentityProviderResult, AuthProviderId, AuthProviderInput, SamlIdentityProviderInput, GetAuthSignInUrl, EnsureIdentityProvider, EnsureSamlIdentityProvider } from '@/types';

interface GetAuthSignInUrlRequest {
  providerId: AuthProviderId;
}
interface SignInUrlResult {
  signInUrl: string;
}

interface EnsureIdentityProviderRequest {
  input: AuthProviderInput;
}
interface EnsureIdentityProviderResponse {
  ensureIdentityProvider: IdentityProviderResult;
}



interface EnsureSamlIdentityProviderRequest {
  input: SamlIdentityProviderInput;
}
interface EnsureSamlIdentityProviderResponse {
  ensureSamlIdentityProvider: IdentityProviderResult;
}




export const useAuthProvider = () => {

    const [signInUrl, { loading: getUrlLoading, error: getUrlError }] = useLazyQuery<SignInUrlResult, GetAuthSignInUrlRequest>(SIGN_IN_URL, {
      onError: (error) => {
        console.error(error);
      },
    });

  const [ensureIdp, { loading: ensureIdpLoading, error: ensureIdpError }] = useMutation<EnsureIdentityProviderResponse, EnsureIdentityProviderRequest>(ENSURE_IDENTITY_PROVIDER, {
    onError: (error) => {
      console.error(error);
    },
  });

  const [ensureSamlIdp, { loading: ensureSamlIdpLoading, error: ensureSamlIdpError }] = useMutation<EnsureSamlIdentityProviderResponse, EnsureSamlIdentityProviderRequest>(
    ENSURE_SAML_IDENTITY_PROVIDER,
    {
      onError: (error) => {
        console.error(error);
      },
    },
  );

  const getSignInUrl: GetAuthSignInUrl = useCallback(async (providerId) => {
    const { data } = await signInUrl({ variables: { providerId } });
    return data?.signInUrl;
  }, []);



  const ensureIdentityProvider: EnsureIdentityProvider = async (input) => {
    const { data } = await ensureIdp({ variables: { input } });
    return data?.ensureIdentityProvider;
  };

  const ensureSamlIdentityProvider: EnsureSamlIdentityProvider = async (input) => {
    const { data } = await ensureSamlIdp({ variables: { input } });
    return data?.ensureSamlIdentityProvider;
  };

  return {
    loading: getUrlLoading || ensureIdpLoading  || ensureSamlIdpLoading,
    error: (getUrlError || ensureIdpError  || ensureSamlIdpError)?.message,
    getSignInUrl,
    ensureIdentityProvider,
    ensureSamlIdentityProvider,
  };
};
