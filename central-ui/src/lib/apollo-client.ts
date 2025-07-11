import { ApolloClient, InMemoryCache, split, HttpLink, from } from '@apollo/client';
import { setContext } from '@apollo/client/link/context';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { createClient } from 'graphql-ws';
import { getMainDefinition } from '@apollo/client/utilities';
import { API } from '@/utils';
import { useUserStore } from '@/store';

const authLink = setContext((_, { headers }) => {
  const token = useUserStore.getState().token;
  return {
    headers: {
      ...headers,
      authorization: token ? `Bearer ${token}` : '',
    },
  };
});

const httpLink = new HttpLink({
  uri: API.GRAPHQL_HTTP,
  credentials: 'same-origin',
});

const wsLink = new GraphQLWsLink(
  createClient({
    url: API.GRAPHQL_WS,
  }),
);

const splitLink = split(
  ({ query }) => {
    const definition = getMainDefinition(query);
    return definition.kind === 'OperationDefinition' && definition.operation === 'subscription';
  },
  wsLink,
  from([authLink, httpLink]),
);

const client = new ApolloClient({
  link: splitLink,
  defaultOptions: {
    watchQuery: { fetchPolicy: 'cache-and-network' },
    query: { fetchPolicy: 'cache-first' },
    mutate: { fetchPolicy: 'network-only' },
  },
  cache: new InMemoryCache({
    addTypename: false,
  }),
});

export default client;
