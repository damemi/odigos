'use client';

import React, { useEffect, useState } from 'react';
import { useAuth } from '@/hooks';
import styled from 'styled-components';
import { OdigosLogo } from '@odigos/ui-kit/icons';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { Button, Input, NotificationNote } from '@odigos/ui-kit/components';
import { Container, Card, Title, Subtitle, CenterThis } from '../signin/signin.styled';

const UpdatePasswordButton = styled(Button)`
  margin-top: 24px;
  padding: 10px 32px;
`;

const Form = styled.form`
  display: flex;
  flex-direction: column;
  gap: 16px;
`;

export default function UpdatePasswordPage() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const { setInitialPassword, loading, error, getLastAuthEmail } = useAuth();
  const [confirmPassword, setConfirmPassword] = useState('');
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const lastEmail = getLastAuthEmail();
    if (lastEmail) setEmail(lastEmail);
  }, [getLastAuthEmail]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (password !== confirmPassword) {
      setFormError(STRINGS.PASSWORDS_DO_NOT_MATCH);
      return;
    }
    await setInitialPassword(email, password);
  };

  const isButtonDisabled = !email || !password || !confirmPassword || loading;

  return (
    <Container>
      <Card>
        <CenterThis>
          <OdigosLogo />
        </CenterThis>
        <Title>{STRINGS.UPDATE_PASSWORD_TITLE}</Title>
        <Subtitle>{STRINGS.UPDATE_PASSWORD_SUBTITLE}</Subtitle>
        <Form onSubmit={handleSubmit}>
          <Input title={STRINGS.EMAIL_LABEL} required value={email} onChange={(e) => setEmail(e.target.value)} placeholder={STRINGS.EMAIL_PLACEHOLDER} type='email' />
          <Input title={STRINGS.NEW_PASSWORD_LABEL} required value={password} onChange={(e) => setPassword(e.target.value)} placeholder={STRINGS.NEW_PASSWORD_PLACEHOLDER} type='password' />
          <Input
            title={STRINGS.CONFIRM_PASSWORD_LABEL}
            required
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            placeholder={STRINGS.CONFIRM_PASSWORD_PLACEHOLDER}
            type='password'
          />
          {(formError || error) && <NotificationNote type={StatusType.Error} title={STRINGS.AUTH_ERROR_TITLE} message={(formError || error) ?? ''} />}
          <CenterThis>
            <UpdatePasswordButton type='submit' disabled={isButtonDisabled}>
              {loading ? STRINGS.UPDATING_PASSWORD_BUTTON : STRINGS.UPDATE_PASSWORD_BUTTON}
            </UpdatePasswordButton>
          </CenterThis>
        </Form>
      </Card>
    </Container>
  );
}
