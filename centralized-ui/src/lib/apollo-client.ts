import { ApolloClient, InMemoryCache, split, HttpLink } from '@apollo/client';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { createClient } from 'graphql-ws';
import { getMainDefinition } from '@apollo/client/utilities';

// ✅ HTTP Link for Queries/Mutations
const httpLink = new HttpLink({
  uri: 'http://localhost:8081/graphql', // ✅ Change to match your backend
  credentials: 'same-origin',
});

// ✅ WebSocket Link for Subscriptions
const wsLink = new GraphQLWsLink(
  createClient({
    url: 'ws://localhost:8081/graphql', // ✅ Enable WebSocket connection
  }),
);

// ✅ Use WebSockets for Subscriptions, HTTP for Queries/Mutations
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
