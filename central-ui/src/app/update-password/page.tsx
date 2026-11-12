'use client';

import React, { useState } from 'react';
import { useAuth } from '@/hooks';
import { useUserStore } from '@/store';
import { StatusType } from '@odigos/ui-kit/types';
import * as STRINGS from '@/utils/constants/strings';
import { VisualOdigosLogo } from '@odigos/ui-kit/visuals';
import { FlexColumn, FlexRow } from '@odigos/ui-kit/components';
import { Button, Input, Modal, Note } from '@odigos/ui-kit/components';
import { FormTitle, FormSubtitle, FormContainer, ConstrainedWidth, ErrorText } from '@/components/auth/styled';

export default function UpdatePasswordPage() {
  const { lastAuthEmail, pendingOneTimePassword } = useUserStore();
  const { setInitialPassword, loading, error } = useAuth();

  // The temporary password is carried over from sign-in. After a reload it is gone, and after
  // a rejected attempt it may be wrong, so the field is shown in both cases.
  const [oneTimePassword, setOneTimePassword] = useState(pendingOneTimePassword || '');
  const showOneTimePassword = !pendingOneTimePassword || !!error;
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [formError, setFormError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    setFormError(null);
    if (!lastAuthEmail) return setFormError(STRINGS.EMAIL_REQUIRED);
    if (!oneTimePassword) return setFormError(STRINGS.TEMPORARY_PASSWORD_REQUIRED);
    if (!password) return setFormError(STRINGS.PASSWORD_REQUIRED);
    if (password !== confirmPassword) return setFormError(STRINGS.PASSWORDS_DO_NOT_MATCH);

    await setInitialPassword(lastAuthEmail, oneTimePassword, password);
  };

  return (
    <Modal isOpen={true}>
      <FlexRow $alignItems='flex-start'>
        <FormContainer $gap={41} onSubmit={handleSubmit}>
          <FlexColumn $gap={16}>
            <FormTitle>{STRINGS.UPDATE_YOUR_PASSWORD}</FormTitle>
            <FormSubtitle>{STRINGS.UPDATE_PASSWORD_SUBTITLE}</FormSubtitle>
          </FlexColumn>

          <ConstrainedWidth>
            <FlexColumn $gap={12}>
              <Input type='email' label={STRINGS.EMAIL} value={lastAuthEmail || ''} disabled />
              <Note status={StatusType.Default} message='This is the email address that you used to sign in to the application.' />

              {showOneTimePassword && (
                <>
                  <Input
                    type='password'
                    label={STRINGS.TEMPORARY_PASSWORD}
                    required
                    autoComplete='current-password'
                    value={oneTimePassword}
                    onChange={(e) => setOneTimePassword(e.target.value)}
                    placeholder={STRINGS.PASSWORD_PLACEHOLDER}
                  />
                  <Note status={StatusType.Default} message={STRINGS.TEMPORARY_PASSWORD_NOTE} />
                </>
              )}

              <Input type='password' label={STRINGS.NEW_PASSWORD} required value={password} onChange={(e) => setPassword(e.target.value)} placeholder={STRINGS.PASSWORD_PLACEHOLDER} />
              <Input
                type='password'
                label={STRINGS.CONFIRM_PASSWORD}
                required
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                placeholder={STRINGS.PASSWORD_PLACEHOLDER}
              />
            </FlexColumn>
          </ConstrainedWidth>

          <FlexColumn>
            {(formError || error) && <ErrorText>{formError || error}</ErrorText>}
            <Button type='submit' fullWidth disabled={!lastAuthEmail || !oneTimePassword || !password || !confirmPassword} loading={loading} label={STRINGS.UPDATE_PASSWORD} />
          </FlexColumn>
        </FormContainer>

        <VisualOdigosLogo />
      </FlexRow>
    </Modal>
  );
}
