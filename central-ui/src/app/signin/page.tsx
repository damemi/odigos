'use client';

import React, { useState, useEffect } from 'react';
import { useUserStore } from '@/store';
import { AUTH_CODE_QUERY_PARAM } from '@/utils';
import { useAuth, useAuthProvider } from '@/hooks';
import { AuthProviderId, LoginType } from '@/types';
import { Modal } from '@odigos/ui-kit/components/v2';
import { FormContainer } from '@/components/auth/styled';
import { VisualOdigosLogo } from '@odigos/ui-kit/visuals';
import { useRouter, useSearchParams } from 'next/navigation';
import { AuthForm, ChooseForm, OidcForm, SamlForm } from '@/components';
import { CenterThis, FadeLoader, FlexRow } from '@odigos/ui-kit/components';

export default function Page() {
  const router = useRouter();
  const { authProviderId, setAuthProviderId } = useUserStore();
  const { systemConfig, systemConfigLoading, signIn, loading: authLoading, error: authError, getCurrentUser } = useAuth();
  const { getSignInUrl, ensureOidcIdentityProvider, ensureSamlIdentityProvider, loading: authProviderLoading, error: authProviderError } = useAuthProvider();

  const [needToSetAuthProvider, setNeedToSetAuthProvider] = useState<LoginType | null>(null);
  const [authSetupSuccess, setAuthSetupSuccess] = useState<boolean>(false);

  const searchParams = useSearchParams();
  const authCode = searchParams.get(AUTH_CODE_QUERY_PARAM);

  useEffect(() => {
    if (authCode) {
      const signedInUser = getCurrentUser();
      if (!signedInUser) signIn({ code: authCode });
    }
  }, [authCode]);

  const handleCleanup = () => {
    setAuthProviderId(null);
    setNeedToSetAuthProvider(null);
    setAuthSetupSuccess(false);
  };

  const handleSignIn = async (providerId: AuthProviderId, loginType: LoginType) => {
    try {
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
    setAuthProviderId(providerId);
    if (!loginType) return;
    await handleSignIn(providerId, loginType);
  };

  if (systemConfigLoading) {
    return (
      <CenterThis $height='100vh'>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  const isFirstTime = systemConfig && !systemConfig.isInitialized;

  let content = (
    <FormContainer>
      <CenterThis $height='100%'>
        <FadeLoader scale={2} />
      </CenterThis>
    </FormContainer>
  );
  if (authProviderId === null) {
    content = <ChooseForm isFirstTime={!!isFirstTime} loading={authLoading || authProviderLoading} onClick={handleProviderClick} />;
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
