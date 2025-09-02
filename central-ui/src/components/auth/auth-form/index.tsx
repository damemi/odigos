import React, { useState } from 'react';
import { Input, NotificationNote } from '@odigos/ui-kit/components';
import { StatusType } from '@odigos/ui-kit/types';
import { Form, CenterThis, StyledButton } from './signin.styled';
import * as STRINGS from '@/utils/constants/strings';

type AuthFormProps = {
  loading: boolean;
  isFirstTime: boolean;
  error: string | null;
  onSubmit: (email: string, password: string) => void;
};

const AuthForm: React.FC<AuthFormProps> = ({ isFirstTime, onSubmit, loading, error }) => {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');

  const handleSubmit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (isFirstTime && password !== confirmPassword) {
      // TODO: Add a better error handler here
      alert(STRINGS.PASSWORDS_DO_NOT_MATCH);
      return;
    }
    onSubmit(email, password);
  };

  const isButtonDisabled = !email || !password || (isFirstTime && !confirmPassword) || !!loading;

  return (
    <Form onSubmit={handleSubmit}>
      <Input title={STRINGS.EMAIL} required value={email} onChange={(e) => setEmail(e.target.value)} placeholder={STRINGS.EMAIL} type='email' />
      {isFirstTime && <NotificationNote type={StatusType.Default} title={STRINGS.SIGN_UP_ADMIN_DISCLAIMER} />}
      <Input title={STRINGS.PASSWORD} required value={password} onChange={(e) => setPassword(e.target.value)} placeholder={STRINGS.PASSWORD} type={'password'} />
      {isFirstTime && (
        <Input title={STRINGS.CONFIRM_PASSWORD} required value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} placeholder={STRINGS.CONFIRM_PASSWORD} type={'password'} />
      )}
      {error && <NotificationNote type={StatusType.Error} title={STRINGS.AUTH_ERROR} message={error} />}
      <CenterThis>
        <StyledButton type='submit' disabled={isButtonDisabled}>
          {isFirstTime ? STRINGS.SIGN_UP : STRINGS.SIGN_IN}
        </StyledButton>
      </CenterThis>
    </Form>
  );
};

export { AuthForm };
