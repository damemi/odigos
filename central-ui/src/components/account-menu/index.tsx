import React from 'react';
import { useAuth } from '@/hooks';
import { useUserStore } from '@/store';
import { usePopup } from '@odigos/ui-kit/hooks';
import { Divider } from '@odigos/ui-kit/components';
import * as STRINGS from '@/utils/constants/strings';
import styled, { useTheme } from 'styled-components';
import { Button, ButtonVariants, Typography, TypographySize } from '@odigos/ui-kit/components';

const Root = styled.div`
  position: relative;
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

  &:hover {
    opacity: 0.8;
  }
`;

const MenuPanel = styled.div`
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  z-index: ${({ theme }) => theme.v2.zIndex.dropdown};

  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 180px;
  padding: 12px 16px;
  border-radius: 4px;
  background-color: ${({ theme }) => theme.v2.colors.black['500']};
  box-sizing: border-box;
`;

const MenuFooter = styled.div`
  display: flex;
  flex-direction: column;
  gap: 12px;
`;

export const AccountMenu: React.FC = () => {
  const theme = useTheme();

  const { user } = useUserStore();
  const { systemConfig, signOut } = useAuth();
  const { popupRef, popupOpen, setPopupOpen } = usePopup();

  const togglePopup = () => setPopupOpen((prev) => !prev);
  const handleLogout = () => signOut();

  return (
    <Root>
      <UserAvatar onClick={togglePopup}>
        <Typography align='center' color={theme.v2.colors.black['500']}>
          {user?.email ? user.email.charAt(0).toUpperCase() : 'U'}
        </Typography>
      </UserAvatar>

      {popupOpen && (
        <MenuPanel ref={popupRef}>
          <Typography align='center' size={TypographySize.XXS}>
            {user?.email || STRINGS.USER}
          </Typography>
          <Divider />
          <MenuFooter>
            <Button label={STRINGS.SIGN_OUT} onClick={handleLogout} variant={ButtonVariants.Secondary} />
            {systemConfig?.version && (
              <Typography color={theme.v2.colors.grey['200']} size={TypographySize.XXS}>
                {STRINGS.VERSION_LABEL(systemConfig.version)}
              </Typography>
            )}
          </MenuFooter>
        </MenuPanel>
      )}
    </Root>
  );
};
