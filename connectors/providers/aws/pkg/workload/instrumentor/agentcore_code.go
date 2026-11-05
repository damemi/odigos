package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// agentCoreCode is a deployed ZIP opened for reading. Detection reads its central directory and a few small
// members, so only those byte ranges are fetched.
type agentCoreCode interface {
	io.ReaderAt
	Size() int64
}

const (
	// agentCoreCodeBlock is the unit fetched from S3 and cached. The ZIP reader walks the central directory
	// through a small buffer, so blocks turn its many small reads into a few requests.
	agentCoreCodeBlock = 512 << 10
	// agentCoreCodeReadLimit bounds what detection may fetch from one ZIP. A central directory is a few MiB even
	// for tens of thousands of members, and every member detection opens is capped by agentCoreMetadataLimit.
	agentCoreCodeReadLimit = 32 << 20
)

type agentCoreS3Reader interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// agentCoreS3Code reads one S3 object through ranged GETs, caching whole blocks. It is used by one detection at a
// time, under the context of the reconcile that opened it.
type agentCoreS3Code struct {
	ctx                  context.Context
	client               agentCoreS3Reader
	bucket, key, version string
	size                 int64

	mu       sync.Mutex
	blocks   map[int64][]byte
	fetched  int64
	requests int
}

func openAgentCoreS3Code(ctx context.Context, client agentCoreS3Reader, source agentCoreArtifactRef) (*agentCoreS3Code, error) {
	in := &s3.HeadObjectInput{Bucket: aws.String(source.Bucket), Key: aws.String(source.Key)}
	if source.Version != "" {
		in.VersionId = aws.String(source.Version)
	}
	head, err := client.HeadObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("read AgentCore code ZIP: %w", err)
	}
	size := aws.ToInt64(head.ContentLength)
	if size <= 0 {
		return nil, fmt.Errorf("%w: AgentCore code ZIP is empty", ErrUnsupportedInstrumentation)
	}
	return &agentCoreS3Code{ctx: ctx, client: client, bucket: source.Bucket, key: source.Key, version: source.Version,
		size: size, blocks: map[int64][]byte{}}, nil
}

func (c *agentCoreS3Code) Size() int64 { return c.size }

// stats reports the bytes and requests fetched so far.
func (c *agentCoreS3Code) stats() (int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched, c.requests
}

func (c *agentCoreS3Code) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= c.size {
		return 0, io.EOF
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	end := min(off+int64(len(p)), c.size)
	// One request covers every missing block the read needs, from the first missing one to the last.
	first, last := int64(-1), int64(-1)
	for b := off / agentCoreCodeBlock; b <= (end-1)/agentCoreCodeBlock; b++ {
		if _, ok := c.blocks[b]; !ok {
			if first < 0 {
				first = b
			}
			last = b
		}
	}
	if first >= 0 {
		if err := c.fetch(first, last); err != nil {
			return 0, err
		}
	}
	n := 0
	for pos := off; pos < end; {
		b := pos / agentCoreCodeBlock
		data := c.blocks[b]
		start := pos - b*agentCoreCodeBlock
		copied := copy(p[n:], data[start:])
		if copied == 0 {
			return n, io.ErrUnexpectedEOF
		}
		n += copied
		pos += int64(copied)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// fetch reads blocks first through last in one ranged GET.
func (c *agentCoreS3Code) fetch(first, last int64) error {
	start := first * agentCoreCodeBlock
	stop := min((last+1)*agentCoreCodeBlock, c.size) // exclusive
	if c.fetched+(stop-start) > agentCoreCodeReadLimit {
		return fmt.Errorf("%w: reading the AgentCore ZIP's index needs more than %d MiB", ErrUnsupportedInstrumentation, agentCoreCodeReadLimit>>20)
	}
	in := &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(c.key), Range: aws.String(fmt.Sprintf("bytes=%d-%d", start, stop-1))}
	if c.version != "" {
		in.VersionId = aws.String(c.version)
	}
	out, err := c.client.GetObject(c.ctx, in)
	if err != nil {
		return fmt.Errorf("read AgentCore code ZIP: %w", err)
	}
	defer out.Body.Close()
	data := make([]byte, stop-start)
	if _, err := io.ReadFull(out.Body, data); err != nil {
		return fmt.Errorf("read AgentCore code ZIP: %w", err)
	}
	c.fetched += stop - start
	c.requests++
	for b := first; b <= last; b++ {
		lo := (b - first) * agentCoreCodeBlock
		hi := min(lo+agentCoreCodeBlock, int64(len(data)))
		c.blocks[b] = data[lo:hi]
	}
	return nil
}
