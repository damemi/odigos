import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

const SOURCES_SCOPE_FIELDS = `
  sources { namespace kind name }
  namespaces
  languages
`;

// v1.26+: sourceScopes shape. No queryParams/grpc — those fields were not in
// the GraphQL schema until odigos v1.32 (merged to main after v1.31.2).
const NOISY_OPERATION_FIELDS_V126 = `
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

// v1.32+: HTTP queryParams + gRPC head-sampling matchers on noisy ops.
const NOISY_OPERATION_FIELDS_V132 = `
  ruleId
  name
  disabled
  sourceScopes { ${SOURCES_SCOPE_FIELDS} }
  operation {
    httpServer { route routePrefix method queryParams { name valueExact } }
    httpClient { serverAddress templatedPath templatedPathPrefix method }
    grpcServer { method service }
    grpcClient { method service serverAddress }
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

const getSamplingRulesQuery = (noisyFields: string) => `
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
        noisyOperations { ${noisyFields} }
        highlyRelevantOperations { ${HIGHLY_RELEVANT_OPERATION_FIELDS} }
        costReductionRules { ${COST_REDUCTION_RULE_FIELDS} }
      }
    }
  }`;

export const GET_SAMPLING_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // The `sourceScopes { sources/namespaces/languages }` shape only exists
    // from v1.26. v1.24/v1.25 expose the older `SourcesScope` shape
    // (workloadName/Kind/Namespace/Language), so gate this query at v1.26 to
    // avoid sending an unsupported selection set to those proxies.
    'v1.26': getSamplingRulesQuery(NOISY_OPERATION_FIELDS_V126),
    // HTTP queryParams + gRPC head-sampling matchers landed in odigos v1.32
    // (after v1.31.2). Older proxies reject that selection set.
    'v1.32': getSamplingRulesQuery(NOISY_OPERATION_FIELDS_V132),
  },
};
