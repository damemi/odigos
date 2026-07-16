'use client';

import React, { useState, useEffect, useRef } from 'react';
import { useUserStore } from '@/store';
import { AUTH_CODE_QUERY_PARAM } from '@/utils';
import { AUTH_PROVIDERS } from '@/utils/constants/auth-providers';
import { Modal } from '@odigos/ui-kit/components';
import { useAuth, useAuthProvider } from '@/hooks';
import { AuthProviderId, LoginType } from '@/types';
import { FormContainer } from '@/components/auth/styled';
import { VisualOdigosLogo } from '@odigos/ui-kit/visuals';
import { useRouter, useSearchParams } from 'next/navigation';
import { CenterThis, FlexRow, Loader } from '@odigos/ui-kit/components';
import { AuthForm, ChooseForm, OidcForm, SamlForm } from '@/components';

export default function Page() {
  const router = useRouter();
  const { authProviderId, setAuthProviderId } = useUserStore();
  const {
    systemConfig,
    systemConfigLoading,
    signIn,
    loading: authLoading,
    error: authError,
    getCurrentUser,
    clearError,
    ensureIdpLogoutBeforeSso,
    consumeAutoSsoProvider,
  } = useAuth();
  const { getSignInUrl, ensureOidcIdentityProvider, ensureSamlIdentityProvider, loading: authProviderLoading, error: authProviderError } = useAuthProvider();

  const [needToSetAuthProvider, setNeedToSetAuthProvider] = useState<LoginType | null>(null);
  const [authSetupSuccess, setAuthSetupSuccess] = useState<boolean>(false);
  const autoSsoStarted = useRef(false);

  const searchParams = useSearchParams();
  const authCode = searchParams.get(AUTH_CODE_QUERY_PARAM);

  useEffect(() => {
    if (authCode) {
      const signedInUser = getCurrentUser();
      if (!signedInUser) signIn({ code: authCode });
    }
  }, [authCode]);

  const handleCleanup = () => {
    clearError();
    setAuthProviderId(null);
    setNeedToSetAuthProvider(null);
    setAuthSetupSuccess(false);
  };

  const handleSignIn = async (providerId: AuthProviderId, loginType: LoginType) => {
    try {
      // After ACCESS_DENIED, clear Azure/Okta SSO via Keycloak logout before retrying.
      // Returns true when navigating away; SSO resumes via AUTO_SSO_PROVIDER_KEY on return.
      if (await ensureIdpLogoutBeforeSso(providerId)) {
        return;
      }

      const url = await getSignInUrl(providerId);
      if (url) {
        router.replace(url);
      } else {
        setNeedToSetAuthProvider(loginType);
      }
    } catch (_err) {
      console.error(_err);
    }
  };

  const handleProviderClick = async (providerId: AuthProviderId, loginType?: LoginType) => {
    clearError();
    setAuthProviderId(providerId);
    if (!loginType) return;
    await handleSignIn(providerId, loginType);
  };

  // Resume SSO after deferred IdP logout (ACCESS_DENIED → user clicks Azure again → logout → here).
  useEffect(() => {
    if (systemConfigLoading || authCode || autoSsoStarted.current) return;
    const providerId = consumeAutoSsoProvider();
    if (!providerId || providerId === AuthProviderId.EMAIL) return;

    const provider = AUTH_PROVIDERS.find((p) => p.providerId === providerId);
    if (!provider?.loginType) return;

    autoSsoStarted.current = true;
    void handleProviderClick(providerId, provider.loginType);
  }, [systemConfigLoading, authCode]);

  if (systemConfigLoading) {
    return (
      <CenterThis $height='100vh'>
        <Loader withSpinnerOld scaleSpinnerOld={2} />
      </CenterThis>
    );
  }

  const isFirstTime = systemConfig && !systemConfig.isInitialized;

  let content = (
    <FormContainer>
      <CenterThis $height='100%'>
        <Loader withSpinnerOld scaleSpinnerOld={2} />
      </CenterThis>
    </FormContainer>
  );
  if (authProviderId === null) {
    content = <ChooseForm isFirstTime={!!isFirstTime} loading={authLoading || authProviderLoading} error={authError} onClick={handleProviderClick} />;
  } else if (authProviderId === AuthProviderId.EMAIL) {
    content = <AuthForm isFirstTime={!!isFirstTime} loading={authLoading || authProviderLoading} error={authError} onSubmit={signIn} />;
  } else if (needToSetAuthProvider === LoginType.OIDC) {
    content = (
      <OidcForm
        providerId={authProviderId}
        loading={authProviderLoading}
        error={authProviderError}
        onCreate={ensureOidcIdentityProvider}
        isCreateSuccess={authSetupSuccess}
        setIsCreateSuccess={setAuthSetupSuccess}
        onLogin={handleProviderClick}
      />
    );
  } else if (needToSetAuthProvider === LoginType.SAML) {
    content = (
      <SamlForm
        providerId={authProviderId}
        loading={authProviderLoading}
        error={authProviderError}
        onCreate={ensureSamlIdentityProvider}
        isCreateSuccess={authSetupSuccess}
        setIsCreateSuccess={setAuthSetupSuccess}
        onLogin={handleProviderClick}
      />
    );
  }
  return (
    <Modal isOpen={true} onBack={authProviderId && !(authSetupSuccess || authSetupSuccess) ? handleCleanup : undefined}>
      <FlexRow $alignItems='flex-start'>
        {content}
        <VisualOdigosLogo />
      </FlexRow>
    </Modal>
  );
}
