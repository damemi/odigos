import React from 'react';
import { useAuth } from '@/hooks';
import styled from 'styled-components';
import { useUserStore } from '@/store';
import { usePopup } from '@odigos/ui-kit/hooks';
import { Text, Popup, FlexColumn, Divider } from '@odigos/ui-kit/components';

const UserText = styled(Text)`
  font-size: 14px;
  font-weight: 500;
  text-transform: uppercase;
  color: ${({ theme }) => theme.colors.primary};
`;

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

const LogoutButton = styled.button`
  display: flex;
  height: 36px;
  align-items: center;
  padding: 0 8px;
  align-self: stretch;
  border-radius: 24px;
  background: none;
  border: none;
  color: white;
  font-size: 16px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s ease;

  &:hover {
    background: ${({ theme }) => theme.colors.dark_grey};
  }
`;

export const AccountMenu: React.FC = () => {
  const { signOut } = useAuth();
  const { user } = useUserStore();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();

  const getInitial = () => {
    return user?.email ? user.email.charAt(0).toUpperCase() : 'O';
  };

  const handleLogout = () => {
    signOut();
    setPopupOpen(false);
  };

  const onAccountClick = () => {
    setPopupOpen((prev) => !prev);
  };

  return (
    <>
      <UserAvatar onClick={onAccountClick}>
        <UserText>{getInitial()}</UserText>
      </UserAvatar>

      <Popup ref={popupRef} isOpen={popupOpen} top={52} right={44} padding='12px 16px'>
        <div>
          <FlexColumn $gap={8}>
            <Text color='white' size={16} weight={500}>
              {user?.email || 'User'}
            </Text>
            <Divider />

            <LogoutButton onClick={handleLogout}>Sign out</LogoutButton>
          </FlexColumn>
        </div>
      </Popup>
    </>
  );
};
