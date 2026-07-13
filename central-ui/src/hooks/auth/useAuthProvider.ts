import { useCallback } from 'react';
import { useLazyQuery, useMutation } from '@apollo/client/react';
import { ENSURE_OIDC_IDENTITY_PROVIDER, ENSURE_SAML_IDENTITY_PROVIDER, SIGN_IN_URL } from '@/graphql';
import type { IdentityProviderResult, AuthProviderId, OidcIdentityProviderInput, SamlIdentityProviderInput, GetAuthSignInUrl, EnsureOidcIdentityProvider, EnsureSamlIdentityProvider } from '@/types';

interface GetAuthSignInUrlRequest {
  providerId: AuthProviderId;
}
interface SignInUrlResult {
  signInUrl: string;
}

interface EnsureOidcIdentityProviderRequest {
  input: OidcIdentityProviderInput;
}
interface EnsureOidcIdentityProviderResponse {
  ensureOidcIdentityProvider: IdentityProviderResult;
}

interface EnsureSamlIdentityProviderRequest {
  input: SamlIdentityProviderInput;
}
interface EnsureSamlIdentityProviderResponse {
  ensureSamlIdentityProvider: IdentityProviderResult;
}

export const useAuthProvider = () => {
  const [signInUrl, { loading: getUrlLoading, error: getUrlError }] = useLazyQuery<SignInUrlResult, GetAuthSignInUrlRequest>(SIGN_IN_URL);
  const [ensureOidcIdp, { loading: ensureIdpLoading, error: ensureIdpError }] = useMutation<EnsureOidcIdentityProviderResponse, EnsureOidcIdentityProviderRequest>(ENSURE_OIDC_IDENTITY_PROVIDER);
  const [ensureSamlIdp, { loading: ensureSamlIdpLoading, error: ensureSamlIdpError }] = useMutation<EnsureSamlIdentityProviderResponse, EnsureSamlIdentityProviderRequest>(
    ENSURE_SAML_IDENTITY_PROVIDER,
  );

  const getSignInUrl: GetAuthSignInUrl = useCallback(
    async (providerId) => {
      try {
        const { data } = await signInUrl({ variables: { providerId } });
        return data?.signInUrl;
      } catch (error) {
        console.error(error);
        return undefined;
      }
    },
    [signInUrl],
  );

  const ensureOidcIdentityProvider: EnsureOidcIdentityProvider = async (input) => {
    try {
      const { data } = await ensureOidcIdp({ variables: { input } });
      return data?.ensureOidcIdentityProvider;
    } catch (error) {
      console.error(error);
      return undefined;
    }
  };

  const ensureSamlIdentityProvider: EnsureSamlIdentityProvider = async (input) => {
    try {
      const { data } = await ensureSamlIdp({ variables: { input } });
      return data?.ensureSamlIdentityProvider;
    } catch (error) {
      console.error(error);
      return undefined;
    }
  };

  return {
    loading: getUrlLoading || ensureIdpLoading || ensureSamlIdpLoading,
    error: (getUrlError || ensureIdpError || ensureSamlIdpError)?.message,
    getSignInUrl,
    ensureOidcIdentityProvider,
    ensureSamlIdentityProvider,
  };
};
