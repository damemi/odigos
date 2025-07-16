import React, { useState, type FC, useMemo } from 'react';
import type { User } from '@/types';
import Theme from '@odigos/ui-kit/theme';
import { DataCardContent } from './styled';
import { MEMBERS_COUNT, PLATFORMS_COUNT } from '@/utils';
import { MinusCircledIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, IconButton, Input, NoDataFound } from '@odigos/ui-kit/components';

interface TeamCardProps {
  formData: Pick<User, 'teams'>;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const TeamCard: FC<TeamCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();

  const [search, setSearch] = useState('');
  const filteredTeams = useMemo(() => formData.teams?.filter((t) => t.name.toLowerCase().includes(search.toLowerCase())), [formData.teams, search]);

  const teamsCount = formData.teams?.length || 0;
  const filteredTeamsCount = filteredTeams?.length || 0;

  return (
    <DataCard title='Teams' titleBadge={teamsCount} action={<Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />}>
      <DataCardContent $justifyContent={!filteredTeamsCount ? 'center' : ''}>
        {!filteredTeamsCount ? (
          <NoDataFound subTitle={search ? undefined : ''} />
        ) : (
          filteredTeams?.map((t) => (
            <DataFinger
              key={`team-${t.id}`}
              title={t.name}
              subTitle={`${PLATFORMS_COUNT(t.computePlatforms?.length || 0)}, ${MEMBERS_COUNT(t.users?.length || 0)}`}
              icons={[UserGroupIcon]}
              renderActions={() => (
                <IconButton
                  onClick={() =>
                    handleFormChange(
                      'teams',
                      formData.teams?.filter(({ id }) => id !== t.id),
                    )
                  }
                  tooltip='Remove user from team'
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))
        )}
      </DataCardContent>
    </DataCard>
  );
};

export { TeamCard, type TeamCardProps };
