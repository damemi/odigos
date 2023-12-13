# Odiglet Enterprise

## Setup

Enterprise odiglet uses private repos from github: 
- `"github.com/keyval-dev/enterprise-go-instrumentation"`
- `"github.com/keyval-dev/ebpf-java-instrumentation"`

To make it work locally, use the following commands:

```sh
export GOPRIVATE=github.com/keyval-dev/enterprise-go-instrumentation,github.com/keyval-dev/ebpf-java-instrumentation
```
