import React, { useState } from 'react';
import { StatusType } from '@odigos/ui-kit/types';
import { CopyText } from '@odigos/ui-kit/snippets';
import { Note } from '@odigos/ui-kit/components/v2';
import { DONE, SIGN_UP_PASSWORD_DISCLAIMER, USER_CREATED_SUCCESS, USER_CREATED_SUCCESS_SUBTITLE } from '@/utils';
import { DoneButton, PasswordInputWrapper, SuccessContainer, SuccessSubtitle, SuccessTitle } from './create-user-modal.styled';

interface UserCreatedSuccessfullyProps {
  email: string;
  password: string;
  onDone: () => void;
}

export const UserCreatedSuccessfully: React.FC<UserCreatedSuccessfullyProps> = ({ email, password, onDone }) => {
  const [done, setDone] = useState(false);

  return (
    <SuccessContainer>
      <SuccessTitle>{USER_CREATED_SUCCESS}</SuccessTitle>
      <SuccessSubtitle>
        {USER_CREATED_SUCCESS_SUBTITLE(email)
          .split('\n')
          .map((line, i) => (
            <React.Fragment key={i}>
              {line}
              <br />
            </React.Fragment>
          ))}
      </SuccessSubtitle>

      <PasswordInputWrapper>
        <CopyText value={password} callbackCopyDone={() => setDone(true)} />
      </PasswordInputWrapper>
      <Note status={StatusType.Default} message={SIGN_UP_PASSWORD_DISCLAIMER} />

      <DoneButton type='button' onClick={onDone} disabled={!done} label={DONE} />
    </SuccessContainer>
  );
};
