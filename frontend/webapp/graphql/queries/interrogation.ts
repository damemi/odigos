import { gql } from '@apollo/client';

/** Fetches sampleTrace nested on InterrogationTransaction (selected on demand). */
export const GET_INTERROGATION_TRANSACTION_SAMPLE_TRACE = gql`
  query InterrogationTransactionSampleTrace($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      id {
        namespace
        kind
        name
      }
      containers {
        containerName
        interrogationTransactions {
          id
          sampleTrace
        }
      }
    }
  }
`;

/** Fetches flat callTrie nodes nested on InterrogationTransaction (selected on demand). */
export const GET_INTERROGATION_TRANSACTION_CALL_TRIE = gql`
  query InterrogationTransactionCallTrie($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      id {
        namespace
        kind
        name
      }
      containers {
        containerName
        interrogationTransactions {
          id
          callTrie {
            id
            parentId
            name
            frameType
            sampleType
            seenCount
          }
        }
      }
    }
  }
`;
