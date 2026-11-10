import { useMutation, useQuery } from '@apollo/client/react';
import { ENSURE_OIDC_IDENTITY_PROVIDER, ENSURE_SAML_IDENTITY_PROVIDER, GET_IDENTITY_PROVIDERS } from '@/graphql';
import type { IdentityProvider, IdentityProviderResult, OidcIdentityProviderInput, SamlIdentityProviderInput } from '@/types';

// Identity provider settings for the Admin tab in Access management. The backend
// restricts both the query and the mutations to ADMIN/OWNER.
export const useIdentityProvidersCRUD = () => {
  const { data, loading, error, refetch } = useQuery<{ identityProviders: IdentityProvider[] }>(GET_IDENTITY_PROVIDERS, {
    notifyOnNetworkStatusChange: true,
  });

  const [ensureOidcMutation, { loading: savingOidc }] = useMutation<{ ensureOidcIdentityProvider: IdentityProviderResult }, { input: OidcIdentityProviderInput }>(
    ENSURE_OIDC_IDENTITY_PROVIDER,
    // The input carries the client secret; don't keep it in the Apollo cache.
    { fetchPolicy: 'no-cache' },
  );
  const [ensureSamlMutation, { loading: savingSaml }] = useMutation<{ ensureSamlIdentityProvider: IdentityProviderResult }, { input: SamlIdentityProviderInput }>(
    ENSURE_SAML_IDENTITY_PROVIDER,
    { fetchPolicy: 'no-cache' },
  );

  // Returns the error message, or undefined on success.
  const saveOidc = async (input: OidcIdentityProviderInput): Promise<string | undefined> => {
    try {
      const { data } = await ensureOidcMutation({ variables: { input } });
      if (!data?.ensureOidcIdentityProvider.success) return data?.ensureOidcIdentityProvider.error || undefined;
      await refetch();
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  };

  const saveSaml = async (input: SamlIdentityProviderInput): Promise<string | undefined> => {
    try {
      const { data } = await ensureSamlMutation({ variables: { input } });
      if (!data?.ensureSamlIdentityProvider.success) return data?.ensureSamlIdentityProvider.error || undefined;
      await refetch();
    } catch (error) {
      return error instanceof Error ? error.message : String(error);
    }
  };

  return {
    data,
    identityProviders: data?.identityProviders ?? [],
    loading,
    error,
    refetch,
    saveOidc,
    saveSaml,
    saving: savingOidc || savingSaml,
  };
};
