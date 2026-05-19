import React from 'react';
import styled from 'styled-components';
import { UserGroupIcon } from '@odigos/ui-kit/icons';
import { DONE, TEAM_CREATED_SUCCESS, TEAM_CREATED_SUCCESS_SUBTITLE } from '@/utils';
import { DoneButton, SuccessContainer, SuccessSubtitle, SuccessTitle } from './create-team-modal.styled';

interface TeamCreatedSuccessfullyProps {
  name: string;
  onDone: () => void;
}

const IconCircle = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border-radius: 8px;
  background: ${({ theme }) => theme.v2.colors.silver['800']};
`;

export const TeamCreatedSuccessfully: React.FC<TeamCreatedSuccessfullyProps> = ({ name, onDone }) => {
  return (
    <SuccessContainer>
      <IconCircle>
        <UserGroupIcon size={32} />
      </IconCircle>
      <SuccessTitle>{TEAM_CREATED_SUCCESS}</SuccessTitle>
      <SuccessSubtitle>
        {TEAM_CREATED_SUCCESS_SUBTITLE(name)
          .split('\n')
          .map((line, i) => (
            <React.Fragment key={i}>
              {line}
              <br />
            </React.Fragment>
          ))}
      </SuccessSubtitle>

      <DoneButton type='button' onClick={onDone} label={DONE} />
    </SuccessContainer>
  );
};
