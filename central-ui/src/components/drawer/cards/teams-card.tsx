import React, { useMemo, useState, type FC } from 'react';
import type { Team } from '@/types';
import { useTeamsCRUD } from '@/hooks';
import { useUserStore } from '@/store';
import { useTheme } from 'styled-components';
import type { SVG } from '@odigos/ui-kit/types';
import { MEMBERS_COUNT, PLATFORMS_COUNT } from '@/utils';
import { MinWidthWide, Relative } from '@/components/styled';
import { PopupForm, Tooltip } from '@odigos/ui-kit/components';
import { MinusCircledIcon, PlusIcon, UserGroupIcon } from '@odigos/ui-kit/icons';
import { Badge, DropDown, IconButton, IconButtonSize, NoData, Search, Typography, TypographyColor, TypographySize } from '@odigos/ui-kit/components/v2';
import { DataCardContentColumn, DataCardContentRow, ListCard, ListCardHeader, ListCardHeaderLeft, ListCardHeaderRight, ListRow, ListRowMain, ListRowText } from './styled';

interface FormData {
  addTeamIds: string[];
  removeTeamIds: string[];
}

interface TeamsCardProps {
  currentTeams: Team[];
  formData: FormData;
  handleFormChange: (key: keyof FormData, value: string[]) => void;
}

const TeamsCard: FC<TeamsCardProps> = ({ currentTeams, formData, handleFormChange }) => {
  const theme = useTheme();
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

  const RemoveIcon = useMemo<SVG>(() => {
    const Cmp: SVG = (props) => <MinusCircledIcon fill={theme.text.error} {...props} />;
    return Cmp;
  }, [theme]);

  return (
    <ListCard>
      <ListCardHeader>
        <ListCardHeaderLeft>
          <Typography>Teams</Typography>
          <Badge label={currentTeams.length} />
        </ListCardHeaderLeft>
        <ListCardHeaderRight>
          <Search value={search} onChange={setSearch} placeholder='Search...' />
          <Relative>
            {isAdmin && <IconButton icon={PlusIcon} size={IconButtonSize.M} onClick={toggleAdd} />}

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
                <DropDown
                  withSearch
                  withMultiSelect
                  options={teams.map((x) => ({ id: x.id, label: x.name }))}
                  values={formData.addTeamIds}
                  setValues={(ids) => {
                    const filtered = ids.filter((id) => !currentTeams.find((t) => t.id === id));
                    handleFormChange('addTeamIds', filtered);

                    const removed = formData.removeTeamIds.filter((id) => !ids.includes(id));
                    if (removed.length !== formData.removeTeamIds.length) {
                      handleFormChange('removeTeamIds', removed);
                    }
                  }}
                />
              </MinWidthWide>
            </PopupForm>
          </Relative>
        </ListCardHeaderRight>
      </ListCardHeader>

      {!filteredTeams.length ? (
        <DataCardContentRow $justifyContent='center'>
          <NoData subTitle={search ? undefined : ''} />
        </DataCardContentRow>
      ) : (
        <DataCardContentColumn>
          {filteredTeams.map(({ id, name, users, computePlatforms }) => (
            <ListRow key={`team-${id}`}>
              <ListRowMain>
                <UserGroupIcon size={16} />
                <ListRowText>
                  <Typography size={TypographySize.S}>{name}</Typography>
                  <Typography size={TypographySize.XS} color={TypographyColor.Secondary}>
                    {`${PLATFORMS_COUNT(computePlatforms?.length || 0)}, ${MEMBERS_COUNT(users?.length || 0)}`}
                  </Typography>
                </ListRowText>
              </ListRowMain>
              {isAdmin && (
                <Tooltip text='Remove team'>
                  <IconButton
                    icon={RemoveIcon}
                    size={IconButtonSize.S}
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
                  />
                </Tooltip>
              )}
            </ListRow>
          ))}
        </DataCardContentColumn>
      )}
    </ListCard>
  );
};

export { TeamsCard, type TeamsCardProps };
