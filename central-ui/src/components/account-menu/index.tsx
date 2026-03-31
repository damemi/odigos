import React from 'react';
import { useAuth } from '@/hooks';
import { useUserStore } from '@/store';
import { usePopup } from '@odigos/ui-kit/hooks';
import styled, { useTheme } from 'styled-components';
import { Popup, Divider } from '@odigos/ui-kit/components';
import { Button, ButtonVariants, Typography, TypographySize } from '@odigos/ui-kit/components/v2';

const UserAvatar = styled.div`
  display: flex;
  width: 32px;
  height: 32px;
  justify-content: center;
  align-items: center;
  border-radius: 10px;
  background: #50f6e8;
  cursor: pointer;
  transition: opacity 0.2s ease;
  position: relative;

  &:hover {
    opacity: 0.8;
  }
`;

export const AccountMenu: React.FC = () => {
  const theme = useTheme();

  const { signOut } = useAuth();
  const { user } = useUserStore();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();

  const togglePopup = () => setPopupOpen((prev) => !prev);
  const handleLogout = () => signOut();

  return (
    <>
      <UserAvatar onClick={togglePopup}>
        <Typography align='center' color={theme.v2.colors.black['500']}>
          {user?.email ? user.email.charAt(0).toUpperCase() : 'U'}
        </Typography>
      </UserAvatar>

      <Popup ref={popupRef} isOpen={popupOpen} top={52} right={44} padding='12px 16px'>
        <Typography align='center' size={TypographySize.XXS}>
          {user?.email || 'User'}
        </Typography>
        <Divider />
        <Button label='Sign out' onClick={handleLogout} variant={ButtonVariants.Text} />
      </Popup>
    </>
  );
};
