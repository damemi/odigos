import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useQuery, useMutation } from '@apollo/client/react';
import { GET_USERS, CREATE_USER, DELETE_USER, UPDATE_USER } from '@/graphql';
import type { User, CreateUserResult, UserInput, GeneralResult } from '@/types';

export const useUsersCRUD = () => {
  const { addNotification } = useNotificationStore();
  const notifyError = (error: { name?: string; message: string; cause?: unknown }, crudType: Crud) =>
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: (error.cause instanceof Error ? error.cause.message : undefined) || error.message });

  const { data, loading, error, refetch } = useQuery<{ users: User[] }>(GET_USERS);
  const [createUserMutation, { loading: creating }] = useMutation<{ createUser: CreateUserResult }, { input: UserInput }>(CREATE_USER);
  const [updateUserMutation, { loading: updating }] = useMutation<{ updateUser: GeneralResult }, { userID: string; input: UserInput }>(UPDATE_USER);
  const [deleteUserMutation, { loading: deleting }] = useMutation<{ deleteUser: GeneralResult }, { userID: string }>(DELETE_USER);

  const createUser = async (input: UserInput): Promise<CreateUserResult | undefined> => {
    try {
      const { data } = await createUserMutation({ variables: { input } });
      refetch();
      return data?.createUser;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Create);
    }
  };

  const updateUser = async (userID: string, input: UserInput): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await updateUserMutation({ variables: { userID, input } });
      refetch();
      return data?.updateUser;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Update);
    }
  };

  const deleteUser = async (userID: string): Promise<GeneralResult | undefined> => {
    try {
      const { data } = await deleteUserMutation({
        variables: { userID },
        update: (cache) => {
          // Remove the deleted user from the cache
          cache.modify({
            fields: { users: (existingUsers = []) => existingUsers.filter((user: User) => user.id !== userID) },
          });
        },
      });
      refetch();
      return data?.deleteUser;
    } catch (error) {
      notifyError(error instanceof Error ? error : { message: String(error) }, Crud.Delete);
    }
  };

  return {
    users: data?.users || [],
    loading,
    error,
    refetch,
    createUser,
    creating,
    updateUser,
    updating,
    deleteUser,
    deleting,
  };
};
