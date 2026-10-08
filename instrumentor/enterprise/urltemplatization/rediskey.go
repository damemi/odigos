package urltemplatization

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/api/k8sconsts"

	commonurltemplatization "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization"
)

type unmatchedKeyMeta struct {
	Workload      k8sconsts.PodWorkload
	ContainerName string
	Server        bool
}

func parseUnmatchedRedisKey(key string) (unmatchedKeyMeta, error) {
	var remainder string
	var server bool
	switch {
	case strings.HasPrefix(key, commonurltemplatization.UnmatchedPathsRedisKeyPrefixServer):
		remainder = strings.TrimPrefix(key, commonurltemplatization.UnmatchedPathsRedisKeyPrefixServer)
		server = true
	case strings.HasPrefix(key, commonurltemplatization.UnmatchedPathsRedisKeyPrefixClient):
		remainder = strings.TrimPrefix(key, commonurltemplatization.UnmatchedPathsRedisKeyPrefixClient)
		server = false
	default:
		return unmatchedKeyMeta{}, fmt.Errorf("unrecognized unmatched redis key %q", key)
	}

	// remainder = {namespace}/{kind}/{name}/{container}
	parts := strings.SplitN(remainder, "/", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return unmatchedKeyMeta{}, fmt.Errorf("invalid unmatched redis key remainder %q", remainder)
	}
	return unmatchedKeyMeta{
		Workload: k8sconsts.PodWorkload{
			Namespace: parts[0],
			Kind:      k8sconsts.WorkloadKind(parts[1]),
			Name:      parts[2],
		},
		ContainerName: parts[3],
		Server:        server,
	}, nil
}

func workloadKey(w k8sconsts.PodWorkload) string {
	return fmt.Sprintf("%s/%s/%s", w.Namespace, w.Kind, w.Name)
}
