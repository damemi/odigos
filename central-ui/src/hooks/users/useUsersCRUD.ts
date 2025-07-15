import type { User, DeleteUserPayload } from '@/types';
import { Crud, StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { GET_USERS, CREATE_USER, DELETE_USER } from '@/graphql';
import { useQuery, useMutation, type ApolloError } from '@apollo/client';

interface CreateUserVariables {
  email: string;
  password?: string;
  role: string;
  teams: string[];
  computePlatforms: string[];
}

interface CreateUserResponse {
  createUser: {
    success: boolean;
    generatedPassword?: string;
  };
}

interface DeleteUserVariables {
  userID: string;
}

interface DeleteUserResponse {
  deleteUser: DeleteUserPayload;
}

export const useUsersCRUD = () => {
  const { addNotification } = useNotificationStore();
  const onError = (error: ApolloError, crudType: Crud) => {
    addNotification({ type: StatusType.Error, title: error.name || crudType, message: error.cause?.message || error.message });
  };

  const { data, loading, error, refetch } = useQuery<{ users: User[] }>(GET_USERS);
  const [createUserMutation, { loading: creating }] = useMutation<CreateUserResponse, CreateUserVariables>(CREATE_USER, { onError: (error) => onError(error, Crud.Create) });
  const [deleteUserMutation, { loading: deleting }] = useMutation<DeleteUserResponse, DeleteUserVariables>(DELETE_USER, { onError: (error) => onError(error, Crud.Delete) });

  const createUser = async (input: CreateUserVariables): Promise<CreateUserResponse | undefined | null> => {
    const response = await createUserMutation({ variables: input });
    refetch();
    return response.data;
  };

  const deleteUser = async (userID: string): Promise<DeleteUserPayload | undefined | null> => {
    const { data } = await deleteUserMutation({
      variables: { userID },
      update: (cache) => {
        // Remove the deleted user from the cache
        cache.modify({
          fields: {
            users(existingUsers = []) {
              return existingUsers.filter((user: User) => user.id !== userID);
            },
          },
        });
      },
    });

    if (data?.deleteUser) {
      refetch();
      return data.deleteUser;
    }

    return null;
  };

  return {
    users: data?.users || [],
    loading,
    error,
    refetch,
    createUser,
    creating,
    deleteUser,
    deleting,
  };
};
