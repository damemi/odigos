import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// Selection set for the InstrumentationRule's row-per-criterion `sourcesScopes`.
// Each row carries one of (workloadName + workloadKind), workloadNamespace, or
// workloadLanguage. The kit consumes the spec-shape `SourcesScopes` instead, so
// callers must adapt the response via `adaptInstrumentationRuleFromWire`.
const SOURCES_SCOPE_FIELDS = `
  workloadName
  workloadKind
  workloadNamespace
  workloadLanguage
`;

export const GET_INSTRUMENTATION_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.28': `
  query GetInstrumentationRules {
    computePlatform {
      instrumentationRules {
        type
        ruleId
        ruleName
        notes
        disabled
        mutable
        profileName
        sourcesScopes { ${SOURCES_SCOPE_FIELDS} }
        conditions {
          status
          type
          reason
          message

        }
        payloadCollection {
          httpRequest {
            mimeTypes
            maxPayloadLength
            dropPartialPayloads
          }
          httpResponse {
            mimeTypes
            maxPayloadLength
            dropPartialPayloads
          }
          dbQuery {
            maxPayloadLength
            dropPartialPayloads
          }
          messaging {
            maxPayloadLength
            dropPartialPayloads
          }
        }
        codeAttributes {
          column
          filePath
          function
          lineNumber
          namespace
          stacktrace
        }
        headersCollection {
          headerKeys
        }
        customInstrumentations {
          golang {
            packageName
            functionName
            receiverName
            receiverMethodName
          }
          java {
            methodName
            className
          }
        }
      }
    }
  }`,
    'v1.11': `
  query GetInstrumentationRules {
    computePlatform {
      instrumentationRules {
        type
        ruleId
        ruleName
        notes
        disabled
        mutable
        profileName
        conditions {
          status
          type
          reason
          message

        }
        payloadCollection {
          httpRequest {
            mimeTypes
            maxPayloadLength
            dropPartialPayloads
          }
          httpResponse {
            mimeTypes
            maxPayloadLength
            dropPartialPayloads
          }
          dbQuery {
            maxPayloadLength
            dropPartialPayloads
          }
          messaging {
            maxPayloadLength
            dropPartialPayloads
          }
        }
        codeAttributes {
          column
          filePath
          function
          lineNumber
          namespace
          stacktrace
        }
        headersCollection {
          headerKeys
        }
        customInstrumentations {
          golang {
            packageName
            functionName
            receiverName
            receiverMethodName
          }
          java {
            methodName
            className
          }
        }
      }
    }
  }`,
  },
};
