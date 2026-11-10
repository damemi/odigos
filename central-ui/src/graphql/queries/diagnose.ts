import { gql } from '@apollo/client';

/** Central Backend `diagnose` — prepares/caches archive server-side for GET /diagnose/download. */
export const DOWNLOAD_DIAGNOSE = gql`
  query DownloadDiagnose($proxyID: String!, $input: DiagnoseInput, $dryRun: Boolean) {
    diagnose(proxyID: $proxyID, input: $input, dryRun: $dryRun) {
      stats {
        fileCount
        totalSizeBytes
        totalSizeHuman
      }
      includeProfiles
      includeMetrics
      includeSourceWorkloads
      sourceWorkloadNamespaces
    }
  }
`;
