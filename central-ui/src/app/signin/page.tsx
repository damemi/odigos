'use client';

import React, { useState } from 'react';
import { useUserStore } from '@/store';
import { OidcProviderId } from '@/types';
import { useRouter } from 'next/navigation';
import { useSessionRedirects } from '@/hooks';
import { useAuth, useOidcProvider } from '@/hooks';
import { Modal } from '@odigos/ui-kit/components/v2';
import { FormContainer } from '@/components/auth/styled';
import { VisualOdigosLogo } from '@odigos/ui-kit/visuals';
import { AuthForm, ChooseForm, OidcForm } from '@/components';
import { CenterThis, FadeLoader, FlexRow } from '@odigos/ui-kit/components';

export default function Page() {
  useSessionRedirects();

  const router = useRouter();
  const { authProviderId, setAuthProviderId } = useUserStore();
  const { systemConfig, systemConfigLoading, signIn, loading: authLoading, error: authError } = useAuth();
  const { getOidcSignInUrl, ensureIdentityProvider, loading: oidcLoading, error: oidcError } = useOidcProvider();

  const [needToSetupOidc, setNeedToSetupOidc] = useState<boolean>(false);
  const [oidcSetupSuccess, setOidcSetupSuccess] = useState<boolean>(false);

  const handleCleanup = () => {
    setAuthProviderId(null);
    setNeedToSetupOidc(false);
    setOidcSetupSuccess(false);
  };

  const handleProviderClick = async (providerId: OidcProviderId) => {
    setAuthProviderId(providerId);

    if (providerId !== OidcProviderId.EMAIL) {
      try {
        const url = await getOidcSignInUrl(providerId);

        if (url) {
          router.replace(url);
        } else {
          setNeedToSetupOidc(true);
        }
      } catch (_err) {
        console.error(_err);
      }
    }
  };

  if (systemConfigLoading) {
    return (
      <CenterThis $height='100vh'>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  const isFirstTime = systemConfig && !systemConfig.isInitialized;

  return (
    <Modal isOpen={true} onBack={authProviderId && !oidcSetupSuccess ? handleCleanup : undefined}>
      <FlexRow $alignItems='flex-start'>
        {authProviderId === null ? (
          <ChooseForm isFirstTime={!!isFirstTime} loading={authLoading || oidcLoading} onClick={handleProviderClick} />
        ) : authProviderId === OidcProviderId.EMAIL ? (
          <AuthForm isFirstTime={!!isFirstTime} loading={authLoading || oidcLoading} error={authError} onSubmit={signIn} />
        ) : needToSetupOidc ? (
          <OidcForm
            providerId={authProviderId}
            loading={oidcLoading}
            error={oidcError}
            onCreate={ensureIdentityProvider}
            isCreateSuccess={oidcSetupSuccess}
            setIsCreateSuccess={setOidcSetupSuccess}
            onLogin={handleProviderClick}
          />
        ) : (
          <FormContainer>
            <CenterThis $height='100%'>
              <FadeLoader scale={2} />
            </CenterThis>
          </FormContainer>
        )}

        <VisualOdigosLogo />
      </FlexRow>
    </Modal>
  );
}
