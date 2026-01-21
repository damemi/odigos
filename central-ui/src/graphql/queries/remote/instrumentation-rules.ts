import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_INSTRUMENTATION_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
          lastTransitionTime
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
