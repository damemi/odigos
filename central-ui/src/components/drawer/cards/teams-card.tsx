import React, { useState, type FC, useMemo } from 'react';
import type { Team } from '@/types';
import { useTeamsCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import Theme from '@odigos/ui-kit/theme';
import { MEMBERS_COUNT, PLATFORMS_COUNT } from '@/utils';
import { MinWidthWide, Relative } from '@/components/styled';
import { DataCardContentColumn, DataCardContentRow } from './styled';
import { MinusCircledIcon, PlusIcon, SearchIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { DataCard, DataFinger, Dropdown, IconButton, Input, NoDataFound, PopupForm } from '@odigos/ui-kit/components';

interface FormData {
  addTeamIds: string[];
  removeTeamIds: string[];
}

interface TeamsCardProps {
  currentTeams: Team[];
  formData: FormData;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  handleFormChange: (key: keyof FormData, value: any) => void;
}

const TeamsCard: FC<TeamsCardProps> = ({ currentTeams, formData, handleFormChange }) => {
  const theme = Theme.useTheme();
  const { teams } = useTeamsCRUD();
  const { isAdmin } = useUserStore();

  const [search, setSearch] = useState('');
  const [isAdd, setIsAdd] = useState(false);
  const toggleAdd = () => setIsAdd((prev) => !prev);

  const filteredTeams = useMemo(() => {
    const arr =
      currentTeams.filter(({ id, name }) => {
        return !formData.removeTeamIds.includes(id) && name.toLowerCase().includes(search.toLowerCase());
      }) || [];

    formData.addTeamIds.forEach((id) => {
      const team = teams.find((team) => team.id === id);
      const exists = arr.find((team) => team.id === id);
      if (team && !exists) {
        arr.push(team);
      }
    });

    return arr;
  }, [currentTeams, search, teams, formData]);

  return (
    <DataCard
      title='Teams'
      titleBadge={currentTeams.length}
      action={
        <>
          <Input icon={SearchIcon} placeholder='Search...' value={search} onChange={(e) => setSearch(e.target.value)} />

          <Relative>
            {isAdmin && (
              <IconButton size={32} onClick={toggleAdd} withBorder>
                <PlusIcon />
              </IconButton>
            )}

            <PopupForm
              flipX
              clientX={36}
              clientY={0}
              isOpen={isAdd}
              title='Add teams from the list'
              onClose={() => {
                handleFormChange('addTeamIds', []);
                toggleAdd();
              }}
              onSave={() => {
                toggleAdd();
              }}
            >
              <MinWidthWide>
                <Dropdown
                  showSearch
                  isMulti
                  options={teams.map((x) => ({ id: x.id, value: x.name }))}
                  value={formData.addTeamIds.map((id) => {
                    const team = teams.find((team) => team.id === id);
                    return { id, value: team?.name || team?.id };
                  })}
                  onSelect={({ id }) => {
                    if (id) {
                      if (formData.removeTeamIds.includes(id)) {
                        const newRemoveTeamIds = formData.removeTeamIds.filter((str) => str !== id);
                        handleFormChange('removeTeamIds', newRemoveTeamIds);
                      }

                      const foundAdd = formData.addTeamIds.find((str) => str === id);
                      const foundCurrent = currentTeams.find((team) => team.id === id);
                      if (!foundAdd && !foundCurrent) {
                        const newAddTeamIds = [...formData.addTeamIds, id];
                        handleFormChange('addTeamIds', newAddTeamIds);
                      }
                    }
                  }}
                  onDeselect={({ id }) => {
                    if (id) {
                      if (formData.addTeamIds.includes(id)) {
                        const newAddTeamIds = formData.addTeamIds.filter((str) => str !== id);
                        handleFormChange('addTeamIds', newAddTeamIds);
                      }
                      // note: do not handle the 'remove' list here, it's not needed
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </>
      }
    >
      {!filteredTeams.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoDataFound subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredTeams?.map(({ id, name, users, computePlatforms }) => (
            <DataFinger
              key={`team-${id}`}
              title={name}
              subTitle={`${PLATFORMS_COUNT(computePlatforms?.length || 0)}, ${MEMBERS_COUNT(users?.length || 0)}`}
              icons={[UserGroupIcon]}
              renderActions={
                isAdmin
                  ? () => (
                      <IconButton
                        tooltip='Remove team'
                        onClick={() => {
                          if (formData.addTeamIds.includes(id)) {
                            const newAddTeamIds = formData.addTeamIds.filter((str) => str !== id);
                            handleFormChange('addTeamIds', newAddTeamIds);
                          }

                          const foundRemove = formData.removeTeamIds.find((str) => str === id);
                          const foundCurrent = currentTeams.find((team) => team.id === id);
                          if (!foundRemove && foundCurrent) {
                            const newRemoveTeamIds = [...formData.removeTeamIds, id];
                            handleFormChange('removeTeamIds', newRemoveTeamIds);
                          }
                        }}
                      >
                        <MinusCircledIcon fill={theme.text.error} />
                      </IconButton>
                    )
                  : undefined
              }
            />
          ))}
        </DataCardContentColumn>
      )}
    </DataCard>
  );
};

export { TeamsCard, type TeamsCardProps };
