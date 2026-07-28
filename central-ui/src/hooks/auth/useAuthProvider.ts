import { useCallback } from 'react';
import { useLazyQuery, useMutation, useQuery } from '@apollo/client/react';
import { ENSURE_OIDC_IDENTITY_PROVIDER, ENSURE_SAML_IDENTITY_PROVIDER, GET_CONFIGURED_IDENTITY_PROVIDERS, SIGN_IN_URL } from '@/graphql';
import type { IdentityProviderResult, AuthProviderId, OidcIdentityProviderInput, SamlIdentityProviderInput, GetAuthSignInUrl, EnsureOidcIdentityProvider, EnsureSamlIdentityProvider } from '@/types';

interface GetAuthSignInUrlRequest {
  providerId: AuthProviderId;
}
interface SignInUrlResult {
  signInUrl: string;
}

interface ConfiguredIdentityProvidersResult {
  configuredIdentityProviders: AuthProviderId[];
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
  // On error, treat as empty so the chooser falls back to all IdP buttons (setup UX)
  // instead of permanently blocking sign-in.
  const {
    data: configuredIdpsData,
    loading: configuredIdpsLoading,
    error: configuredIdpsError,
  } = useQuery<ConfiguredIdentityProvidersResult>(GET_CONFIGURED_IDENTITY_PROVIDERS);

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
    configuredIdentityProviders: configuredIdpsError ? [] : (configuredIdpsData?.configuredIdentityProviders ?? []),
    configuredIdentityProvidersLoading: configuredIdpsLoading,
    getSignInUrl,
    ensureOidcIdentityProvider,
    ensureSamlIdentityProvider,
  };
};