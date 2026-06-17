import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

const SOURCES_SCOPE_FIELDS = `
  sources { namespace kind name }
  namespaces
  languages
`;

const NOISY_OPERATION_FIELDS = `
  ruleId
  name
  disabled
  sourceScopes { ${SOURCES_SCOPE_FIELDS} }
  operation {
    httpServer { route routePrefix method }
    httpClient { serverAddress templatedPath templatedPathPrefix method }
  }
  percentageAtMost
  notes
`;

const HIGHLY_RELEVANT_OPERATION_FIELDS = `
  ruleId
  name
  disabled
  sourceScopes { ${SOURCES_SCOPE_FIELDS} }
  error
  durationAtLeastMs
  operation {
    httpServer { route routePrefix method }
    kafkaConsumer { kafkaTopic }
    kafkaProducer { kafkaTopic }
  }
  percentageAtLeast
  notes
`;

const COST_REDUCTION_RULE_FIELDS = `
  ruleId
  name
  disabled
  sourceScopes { ${SOURCES_SCOPE_FIELDS} }
  operation {
    httpServer { route routePrefix method }
    kafkaConsumer { kafkaTopic }
    kafkaProducer { kafkaTopic }
  }
  percentageAtMost
  notes
`;

export const GET_SAMPLING_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // The `sourceScopes { sources/namespaces/languages }` shape only exists
    // from v1.26. v1.24/v1.25 expose the older `SourcesScope` shape
    // (workloadName/Kind/Namespace/Language), so gate this query at v1.26 to
    // avoid sending an unsupported selection set to those proxies.
    'v1.26': `
  query GetSamplingRules {
    sampling {
      configs {
        effective {
          k8sHealthProbesSampling {
            enabled
            keepPercentage
          }
        }
      }
      rules {
        id
        name
        noisyOperations { ${NOISY_OPERATION_FIELDS} }
        highlyRelevantOperations { ${HIGHLY_RELEVANT_OPERATION_FIELDS} }
        costReductionRules { ${COST_REDUCTION_RULE_FIELDS} }
      }
    }
  }`,
  },
};
