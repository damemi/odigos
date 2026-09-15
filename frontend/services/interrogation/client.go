// Package interrogation reads transaction function sets from the interrogation
// Redis used by the cluster-collector exporters.
package interrogation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/services"
)

const (
	// Must match odigosinterrogationtracesexporter key format.
	txFunctionsKeyPrefix  = "interrogation:tx:funcs:"
	txCountKeyPrefix      = "interrogation:tx:count:"
	txFnCountsKeyPrefix   = "interrogation:tx:fncounts:"
	txSpansKeyPrefix      = "interrogation:tx:spans:"
	txTrieCountsKeyPrefix = "interrogation:tx:trie:counts:"
	txTrieMetaKeyPrefix   = "interrogation:tx:trie:meta:"

	trieRootParent = "root"
	trieMetaSep    = "\x1f"

	redisDialTimeout  = 5 * time.Second
	redisReadTimeout  = 2 * time.Second
	redisWriteTimeout = 2 * time.Second
)

var (
	// ErrNotEnabled is returned when interrogation is off in effective config.
	ErrNotEnabled = errors.New("interrogation is not enabled")
	// ErrUnavailable is returned when Redis cannot be reached.
	ErrUnavailable = errors.New("interrogation redis is unavailable")
)

// Function is one Redis set member parsed into fields.
type Function struct {
	Name       string
	FrameType  string
	SampleType string
	// SeenCount is how many times this function was observed for the transaction.
	SeenCount int64
}

// Transaction is one Redis key's transaction id and its function set.
type Transaction struct {
	ID string
	// SeenCount is how many times this transaction was observed.
	SeenCount int64
	Functions []Function
}

// CallTrieNode is one flat node in a transaction call-path trie.
// ParentID is empty for roots (Redis parent "root").
type CallTrieNode struct {
	ID         string
	ParentID   string
	Name       string
	FrameType  string
	SampleType string
	SeenCount  int64
}

// ContainerTransactions is the result for one workload container.
type ContainerTransactions struct {
	Namespace     string
	Kind          string
	Name          string
	ContainerName string
	Transactions  []Transaction
}

// Client talks to the interrogation Redis (read-only).
type Client struct {
	rdb *redis.Client
}

// NewClient builds a Redis client for endpoint (host:port). endpoint may be
// empty; List then returns ErrUnavailable. Does not ping at construction time
// so UI bootstrap succeeds when interrogation Redis is not deployed.
func NewClient(endpoint string) *Client {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return &Client{}
	}
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr:         endpoint,
			DialTimeout:  redisDialTimeout,
			ReadTimeout:  redisReadTimeout,
			WriteTimeout: redisWriteTimeout,
		}),
	}
}

// NewClientWithRedis is for tests.
func NewClientWithRedis(rdb *redis.Client) *Client {
	return &Client{rdb: rdb}
}

// Close closes the underlying Redis client.
func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// IsEnabled reports whether interrogation is enabled in effective config.
func IsEnabled(ctx context.Context, c client.Client) (bool, error) {
	cfg, err := services.GetEffectiveConfig(ctx, c)
	if err != nil {
		return false, err
	}
	return EnabledFromOdigosConfig(cfg), nil
}

// EnabledFromOdigosConfig reports whether interrogation is enabled for a loaded config.
func EnabledFromOdigosConfig(cfg *common.OdigosConfiguration) bool {
	return cfg.InterrogationEnabled()
}

// ListContainerTransactions SCANs Redis for keys of the given workload container
// and returns each transaction with its function members.
func (c *Client) ListContainerTransactions(ctx context.Context, namespace, kind, name, containerName string) (*ContainerTransactions, error) {
	if c == nil || c.rdb == nil {
		return nil, ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	if namespace == "" || kind == "" || name == "" || containerName == "" {
		return nil, fmt.Errorf("namespace, kind, name, and containerName are required")
	}

	prefix := workloadContainerPrefix(namespace, kind, name, containerName)
	pattern := prefix + "*"

	keys, err := scanKeys(ctx, c.rdb, pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	out := &ContainerTransactions{
		Namespace:     namespace,
		Kind:          kind,
		Name:          name,
		ContainerName: containerName,
		Transactions:  make([]Transaction, 0, len(keys)),
	}
	for _, key := range keys {
		txID, ok := transactionIDFromKey(key, prefix)
		if !ok || txID == "" {
			continue
		}
		members, err := c.rdb.SMembers(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("%w: smembers %s: %v", ErrUnavailable, key, err)
		}
		txSeen, err := c.rdb.Get(ctx, txCountKey(namespace, kind, name, containerName, txID)).Int64()
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("%w: get tx count: %v", ErrUnavailable, err)
		}
		fnCounts, err := c.rdb.HGetAll(ctx, txFnCountsKey(namespace, kind, name, containerName, txID)).Result()
		if err != nil {
			return nil, fmt.Errorf("%w: hgetall fn counts: %v", ErrUnavailable, err)
		}
		fns := make([]Function, 0, len(members))
		for _, m := range members {
			fn, ok := parseFunctionMember(m)
			if !ok {
				continue
			}
			if raw, ok := fnCounts[m]; ok {
				if n, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
					fn.SeenCount = n
				}
			}
			fns = append(fns, fn)
		}
		out.Transactions = append(out.Transactions, Transaction{
			ID:        txID,
			SeenCount: txSeen,
			Functions: fns,
		})
	}
	return out, nil
}

// GetTransactionSampleTrace returns the OTLP JSON traces sample stored for the
// transaction, or empty string when none exists.
func (c *Client) GetTransactionSampleTrace(ctx context.Context, namespace, kind, name, containerName, txID string) (string, error) {
	if c == nil || c.rdb == nil {
		return "", ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	txID = strings.TrimSpace(txID)
	if namespace == "" || kind == "" || name == "" || containerName == "" || txID == "" {
		return "", fmt.Errorf("namespace, kind, name, containerName, and transactionId are required")
	}

	raw, err := c.rdb.Get(ctx, txSpansKey(namespace, kind, name, containerName, txID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: get tx spans: %v", ErrUnavailable, err)
	}
	return raw, nil
}

// GetTransactionCallTrie returns flat call-path trie nodes for a transaction.
// ok is false when neither trie hash exists (caller should treat as null).
func (c *Client) GetTransactionCallTrie(ctx context.Context, namespace, kind, name, containerName, txID string) (nodes []CallTrieNode, ok bool, err error) {
	if c == nil || c.rdb == nil {
		return nil, false, ErrUnavailable
	}
	namespace = strings.TrimSpace(namespace)
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	containerName = strings.TrimSpace(containerName)
	txID = strings.TrimSpace(txID)
	if namespace == "" || kind == "" || name == "" || containerName == "" || txID == "" {
		return nil, false, fmt.Errorf("namespace, kind, name, containerName, and transactionId are required")
	}

	counts, err := c.rdb.HGetAll(ctx, txTrieCountsKey(namespace, kind, name, containerName, txID)).Result()
	if err != nil {
		return nil, false, fmt.Errorf("%w: hgetall trie counts: %v", ErrUnavailable, err)
	}
	meta, err := c.rdb.HGetAll(ctx, txTrieMetaKey(namespace, kind, name, containerName, txID)).Result()
	if err != nil {
		return nil, false, fmt.Errorf("%w: hgetall trie meta: %v", ErrUnavailable, err)
	}
	if len(counts) == 0 && len(meta) == 0 {
		return nil, false, nil
	}
	return buildCallTrie(meta, counts), true, nil
}

func workloadContainerPrefix(namespace, kind, name, containerName string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:", txFunctionsKeyPrefix, namespace, kind, name, containerName)
}

func txCountKey(namespace, kind, name, containerName, txID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:%s", txCountKeyPrefix, namespace, kind, name, containerName, txID)
}

func txFnCountsKey(namespace, kind, name, containerName, txID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:%s", txFnCountsKeyPrefix, namespace, kind, name, containerName, txID)
}

func txSpansKey(namespace, kind, name, containerName, txID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:%s", txSpansKeyPrefix, namespace, kind, name, containerName, txID)
}

func txTrieCountsKey(namespace, kind, name, containerName, txID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:%s", txTrieCountsKeyPrefix, namespace, kind, name, containerName, txID)
}

func txTrieMetaKey(namespace, kind, name, containerName, txID string) string {
	return fmt.Sprintf("%s%s/%s/%s/%s:%s", txTrieMetaKeyPrefix, namespace, kind, name, containerName, txID)
}

func transactionIDFromKey(key, prefix string) (string, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	return key[len(prefix):], true
}

// parseFunctionMember parses "name|frameType|sampleType".
// Stack separators and other non-members return ok=false.
func parseFunctionMember(member string) (Function, bool) {
	parts := strings.SplitN(member, "|", 3)
	if len(parts) != 3 || parts[0] == "" {
		return Function{}, false
	}
	return Function{
		Name:       parts[0],
		FrameType:  parts[1],
		SampleType: parts[2],
	}, true
}

func scanKeys(ctx context.Context, rdb *redis.Client, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys, nil
}
