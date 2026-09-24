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

// Dynamic instrumentation-rule-type catalog (YAML-driven), mirroring
// GET_ACTION_TYPES. K8s: introduced in odigos v1.31 (the `instrumentationRuleTypes`
// resolver is unreleased as of v1.30.x). Proxies older than that have no bucket here ->
// `getVersionedQuery` returns undefined -> `canRun` is false and the kit falls
// back to its hard-coded INSTRUMENTATION_RULE_OPTIONS.
export const GET_INSTRUMENTATION_RULE_TYPES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.31': `
  query GetInstrumentationRuleTypes {
    instrumentationRuleTypes {
      type
      displayName
      description
      supportedLanguages
      docsUrl
      fields {
        name
        displayName
        componentType
        componentProperties
        initialValue
        renderCondition
      }
    }
  }`,
  },
  // VM proxies resolve the rule catalog from the versioned vmagent
  // `instrumentationRuleTypes` dialect document (see the VmSpec on
  // GET_INSTRUMENTATION_RULE_TYPES in odigos-api-adapter).
};

export const GET_INSTRUMENTATION_RULES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `managedBy` (odigos.io/managed-by owner) introduced in odigos v1.38.0-pre5.
    'v1.38': `
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
        managedBy
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
          php {
            className
            functionName
          }
        }
      }
    }
  }`,
    'v1.33': `
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
          php {
            className
            functionName
          }
        }
      }
    }
  }`,
    // `sourcesScopes` replaced the legacy `workloads` field in odigos v1.26 — the two
    // never coexist on the output type, so this bucket must start exactly at v1.26.
    // (It was previously keyed 'v1.28', which sent v1.26/v1.27 proxies down the v1.11
    // bucket and left them with no scope field selected at all.)
    'v1.26': `
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
        # Pre-v1.26 scope. sourcesScopes doesn't exist yet on these proxies, and
        # workloads is removed at v1.26, so this selection is valid for exactly the
        # v1.11..v1.25 range this bucket serves. The kit needs it to echo the scope
        # back on update: updateInstrumentationRule is a full replace, so a rule
        # whose scope was never loaded gets it cleared on any edit.
        workloads {
          namespace
          kind
          name
        }
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
