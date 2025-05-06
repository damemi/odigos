import { ApolloClient, InMemoryCache, split, HttpLink } from '@apollo/client';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { createClient } from 'graphql-ws';
import { getMainDefinition } from '@apollo/client/utilities';
import { API } from '@/utils';

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
  httpLink,
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
