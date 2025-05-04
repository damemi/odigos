import { ApolloClient, InMemoryCache, split, HttpLink } from '@apollo/client';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { createClient } from 'graphql-ws';
import { getMainDefinition } from '@apollo/client/utilities';

const backendHttpUrl = process.env.NEXT_PUBLIC_BACKEND_URL || 'http://localhost:8081/graphql';
const backendWsUrl = process.env.NEXT_PUBLIC_BACKEND_WS_URL || 'ws://localhost:8081/graphql';

const httpLink = new HttpLink({
  uri: backendHttpUrl,
  credentials: 'same-origin',
});

const wsLink = new GraphQLWsLink(
  createClient({
    url: backendWsUrl,
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
