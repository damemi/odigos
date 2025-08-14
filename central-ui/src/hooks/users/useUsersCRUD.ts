import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useQuery, useMutation, type ApolloError } from '@apollo/client';
import { GET_USERS, CREATE_USER, DELETE_USER, UPDATE_USER } from '@/graphql';
import type { User, CreateUserResult, UserInput, GeneralResult } from '@/types';

export const useUsersCRUD = () => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });

  const { data, loading, error, refetch } = useQuery<{ users: User[] }>(GET_USERS);
  const [createUserMutation, { loading: creating }] = useMutation<{ createUser: CreateUserResult }, { input: UserInput }>(CREATE_USER, {
    onError: (error) => onError(error, Crud.Create),
  });
  const [updateUserMutation, { loading: updating }] = useMutation<{ updateUser: GeneralResult }, { userID: string; input: UserInput }>(UPDATE_USER, {
    onError: (error) => onError(error, Crud.Update),
  });
  const [deleteUserMutation, { loading: deleting }] = useMutation<{ deleteUser: GeneralResult }, { userID: string }>(DELETE_USER, {
    onError: (error) => onError(error, Crud.Delete),
  });

  const createUser = async (input: UserInput): Promise<CreateUserResult | undefined> => {
    const { data } = await createUserMutation({ variables: { input } });
    refetch();

    return data?.createUser;
  };

  const updateUser = async (userID: string, input: UserInput): Promise<GeneralResult | undefined> => {
    const { data } = await updateUserMutation({ variables: { userID, input } });
    refetch();

    return data?.updateUser;
  };

  const deleteUser = async (userID: string): Promise<GeneralResult | undefined> => {
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
