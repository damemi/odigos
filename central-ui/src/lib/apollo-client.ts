import { useUserStore } from '@/store';
import { API, ROUTES } from '@/utils';
import { createClient } from 'graphql-ws';
import { onError } from '@apollo/client/link/error';
import { setContext } from '@apollo/client/link/context';
import { getMainDefinition } from '@apollo/client/utilities';
import { GraphQLWsLink } from '@apollo/client/link/subscriptions';
import { ApolloClient, InMemoryCache, split, HttpLink, from } from '@apollo/client';

const redirectToSignIn = () => {
  try {
    useUserStore.getState().clearAuth();
  } catch {}
  if (typeof window !== 'undefined') {
    window.location.assign(ROUTES.SIGN_IN);
  }
};

const isAscii = (value: string) => {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code > 0x7e) return false;
  }
  return true;
};

const isAuthErrorMessage = (message?: string) => {
  if (!message) return false;
  const lower = message.toLowerCase();
  return lower.includes('authentication required') || lower.includes('authentication expired') || lower.includes('http 401');
};

const errorLink = onError(({ graphQLErrors, networkError }) => {
  if (graphQLErrors && graphQLErrors.length > 0) {
    const shouldRedirect = graphQLErrors.some((e) => isAuthErrorMessage(e.message));

    if (shouldRedirect) redirectToSignIn();
  }

  // Some auth failures might surface as network 401s
  // Apollo's HttpLink sets networkError with a statusCode when available
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const ne: any = networkError as any;
  if (ne && (ne.statusCode === 401 || isAuthErrorMessage(ne?.message))) {
    redirectToSignIn();
  }
});

const authLink = setContext((_, { headers }) => {
  const token = useUserStore.getState().token;

  if (token && !isAscii(token)) {
    redirectToSignIn();
    return { headers: { ...headers } };
  }

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
  from([errorLink, authLink, httpLink]),
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
