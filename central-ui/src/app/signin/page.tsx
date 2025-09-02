'use client';

import React from 'react';
import { useAuth } from '@/hooks';
import * as STRINGS from '@/utils';
import { AuthForm } from '@/components';
import { OdigosLogo } from '@odigos/ui-kit/icons';
import { FadeLoader } from '@odigos/ui-kit/components';
import { Container, Card, Title, Subtitle, CenterThis } from './signin.styled';

export default function SignInPage() {
  const { systemConfig, systemConfigLoading, loading, error, signIn } = useAuth();
  if (systemConfigLoading) {
    return (
      <Container>
        <FadeLoader />
      </Container>
    );
  }
  const isFirstTime = systemConfig && !systemConfig.isInitialized;

  const handleSubmit = (email: string, password: string) => {
    signIn(email, password);
  };

  return (
    <Container>
      <Card>
        <CenterThis>
          {/* TODO: need to be bigger by design*/}
          <OdigosLogo />
        </CenterThis>
        <Title>{isFirstTime ? STRINGS.SIGN_UP : STRINGS.SIGN_IN}</Title>
        <Subtitle>{isFirstTime ? STRINGS.SIGN_UP_SUBTITLE : STRINGS.SIGN_IN_SUBTITLE}</Subtitle>
        <AuthForm isFirstTime={!!isFirstTime} onSubmit={handleSubmit} loading={loading} error={error} />
      </Card>
    </Container>
  );
}
