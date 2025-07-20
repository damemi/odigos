import React, { useState, type FC, useMemo } from 'react';
import type { Team } from '@/types';
import { useTeamsCRUD } from '@/hooks';
import Theme from '@odigos/ui-kit/theme';
import { useGenericForm } from '@odigos/ui-kit/hooks';
import { MEMBERS_COUNT, PLATFORMS_COUNT } from '@/utils';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';

interface TeamsCardProps {
  formData: {
    teams?: Team[];
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: string, value: any) => void;
}

const TeamsCard: FC<TeamsCardProps> = ({ formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { teams } = useTeamsCRUD();

  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);
  const { formData: tempFormData, handleFormChange: handleTempFormChange, resetFormData: resetTempFormData } = useGenericForm(formData);

  const [search, setSearch] = useState('');
  const filteredTeams = useMemo(() => formData.teams?.filter(({ name }) => name.toLowerCase().includes(search.toLowerCase())), [formData.teams, search]);

  const teamsCount = formData.teams?.length || 0;
  const filteredTeamsCount = filteredTeams?.length || 0;

  return (
    <DataCard
      title='Teams'
      titleBadge={teamsCount}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            <IconButton size={32} onClick={toggleAdd} withBorder>
              <PlusIcon />
            </IconButton>

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add teams from the list'
              onClose={() => {
                resetTempFormData();
                toggleAdd();
              }}
              onSave={() => {
                handleFormChange('teams', tempFormData.teams);
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={teams.map((x) => ({ id: x.id, value: x.name }))}
                  value={tempFormData.teams?.map((x) => ({ id: x.id, value: x.name }))}
                  onSelect={({ id }) => {
                    const fullTeam = teams.find((team) => team.id === id);
                    if (fullTeam) {
                      handleTempFormChange('teams', [...(tempFormData.teams || []), fullTeam]);
                    }
                  }}
                  onDeselect={({ id }) => {
                    handleTempFormChange(
                      'teams',
                      tempFormData.teams?.filter((x) => x.id !== id),
                    );
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredTeamsCount ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredTeams?.map((x) => (
            <DataFinger
              key={`team-${x.id}`}
              title={x.name}
              subTitle={`${PLATFORMS_COUNT(x.computePlatforms?.length || 0)}, ${MEMBERS_COUNT(x.users?.length || 0)}`}
              icons={[UserGroupIcon]}
              renderActions={() => (
                <IconButton
                  tooltip='Remove team'
                  onClick={() => {
                    handleFormChange(
                      'teams',
                      formData.teams?.filter(({ id }) => id !== x.id),
                    );
                  }}
                >
                  <MinusCircledIcon fill={theme.text.error} />
                </IconButton>
              )}
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { TeamsCard, type TeamsCardProps };
