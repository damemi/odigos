import React from 'react';
import { UserGroupIcon } from '@odigos/ui-kit/icons';
import { IconWrapped } from '@odigos/ui-kit/components';
import { SuccessContainer, SuccessTitle, SuccessSubtitle, DoneButton } from './create-team-modal.styled';
import { TEAM_CREATED_SUCCESS, TEAM_CREATED_SUCCESS_SUBTITLE, DONE } from '@/utils';

interface TeamCreatedSuccessfullyProps {
  name: string;
  onDone: () => void;
}

export const TeamCreatedSuccessfully: React.FC<TeamCreatedSuccessfullyProps> = ({ name, onDone }) => {
  return (
    <SuccessContainer>
      <IconWrapped size={48} icon={() => <UserGroupIcon size={32} />} />
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

      <DoneButton type='button' onClick={onDone}>
        {DONE}
      </DoneButton>
    </SuccessContainer>
  );
};
