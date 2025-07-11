import type { User, DeleteUserPayload } from '@/types';
import { GET_USERS, CREATE_USER, DELETE_USER } from '@/graphql';
import { useQuery, useMutation } from '@apollo/client';

interface CreateUserResponse {
  data?: {
    createUser?: {
      success: boolean;
      generatedPassword?: string;
    };
  };
}

interface DeleteUserResponse {
  deleteUser: DeleteUserPayload;
}

interface DeleteUserVariables {
  userID: string;
}

export const useUsersCRUD = () => {
  const { data, loading, error, refetch } = useQuery<{ users: User[] }>(GET_USERS);
  const [createUserMutation, { loading: creating }] = useMutation(CREATE_USER);
  const [deleteUserMutation, { loading: deleting }] = useMutation<DeleteUserResponse, DeleteUserVariables>(DELETE_USER);

  const createUser = async (input: { email: string; password?: string; role: string }): Promise<CreateUserResponse> => {
    const response = await createUserMutation({ variables: input });
    refetch();
    return response as CreateUserResponse;
  };

  const deleteUser = async (userID: string): Promise<DeleteUserPayload> => {
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

    if (!data?.deleteUser) {
      throw new Error('Failed to delete user');
    }
    refetch();
    return data.deleteUser;
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
