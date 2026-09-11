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
