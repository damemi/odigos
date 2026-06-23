import React, { useState } from 'react';
import { CopyIcon } from '@odigos/ui-kit/icons';
import { StatusType } from '@odigos/ui-kit/types';
import { IconButton, IconButtonSize, Note } from '@odigos/ui-kit/components/v2';
import { DONE, SIGN_UP_PASSWORD_DISCLAIMER, USER_CREATED_SUCCESS, USER_CREATED_SUCCESS_SUBTITLE } from '@/utils';
import { DoneButton, PasswordBox, PasswordInputWrapper, PasswordText, SuccessContainer, SuccessSubtitle, SuccessTitle } from './create-user-modal.styled';

interface UserCreatedSuccessfullyProps {
  email: string;
  password: string;
  onDone: () => void;
}

const copyToClipboard = async (value: string) => {
  if (navigator?.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // fall through to legacy fallback
    }
  }

  const textarea = document.createElement('textarea');
  textarea.value = value;
  document.body.appendChild(textarea);
  textarea.select();
  try {
    document.execCommand('copy');
  } catch {
    // ignore
  }
  document.body.removeChild(textarea);
};

export const UserCreatedSuccessfully: React.FC<UserCreatedSuccessfullyProps> = ({ email, password, onDone }) => {
  const [done, setDone] = useState(false);

  const handleCopy = async () => {
    await copyToClipboard(password);
    setDone(true);
  };

  return (
    <SuccessContainer>
      <SuccessTitle>{USER_CREATED_SUCCESS}</SuccessTitle>
      <SuccessSubtitle>{USER_CREATED_SUCCESS_SUBTITLE(email)}</SuccessSubtitle>

      <PasswordInputWrapper>
        <PasswordBox>
          <PasswordText>{password}</PasswordText>
          <IconButton icon={CopyIcon} size={IconButtonSize.S} onClick={handleCopy} />
        </PasswordBox>
      </PasswordInputWrapper>
      <Note status={StatusType.Default} message={SIGN_UP_PASSWORD_DISCLAIMER} />

      <DoneButton type='button' onClick={onDone} disabled={!done} label={DONE} />
    </SuccessContainer>
  );
};
