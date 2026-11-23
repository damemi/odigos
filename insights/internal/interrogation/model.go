package interrogation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"

	apmodel "github.com/odigos-io/odigos/insights/internal/autopilot/model"
)

// modelTimeout bounds one model call, as the loop's client does.
const modelTimeout = 2 * time.Minute

// maxErrorBody is how much of a refused call's body is kept, as in the loop.
const maxErrorBody = 512

var errModelPaused = errors.New("model calls are paused")

// Pause is the loop's model-call pause (*loop.Manager). Runs and the loop
// share it, so a quota, budget or auth failure seen by either stops both.
type Pause interface {
	ModelPausedUntil(now time.Time) time.Time
	NoteModelError(err error, now time.Time) time.Time
	NoteModelSuccess(now time.Time)
}

// ModelCallError is a failed model call. Paused is set when the shared pause
// is in force, either already or because of this error.
type ModelCallError struct {
	Err    error
	Paused bool
	Until  time.Time
}

func (e *ModelCallError) Error() string {
	if e.Paused {
		return fmt.Sprintf("model call: %v (paused until %s)", e.Err, e.Until.UTC().Format(time.RFC3339))
	}
	return "model call: " + e.Err.Error()
}

func (e *ModelCallError) Unwrap() error { return e.Err }

// NewModel returns ADK's OpenAI client pointed at the LiteLLM gateway, with
// errors mapped to the loop's types and the loop's shared pause applied.
func NewModel(ctx context.Context, apiKey string, pause Pause) (model.LLM, error) {
	return newModel(ctx, apmodel.LiteLLMServiceURL, apiKey, pause)
}

func newModel(ctx context.Context, baseURL, apiKey string, pause Pause) (model.LLM, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		// openai-go would fall back to OPENAI_API_KEY and send it to the gateway.
		return nil, errors.New("litellm master key is empty")
	}
	m, err := openaimodel.NewModel(ctx, apmodel.LiteLLMModelName, &openaimodel.ClientConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		API:     openaimodel.APIChatCompletions,
		// One attempt per call, as in the loop. A failed call is retried by
		// the supervisor, which counts it.
		Options: []option.RequestOption{option.WithMaxRetries(0)},
	})
	if err != nil {
		return nil, err
	}
	return &gatewayModel{LLM: m, pause: pause, now: time.Now}, nil
}

// gatewayModel refuses to call while the shared pause is in force and turns
// the client's errors into the loop's, so the loop's classifier decides what
// pauses every caller.
type gatewayModel struct {
	model.LLM
	pause Pause
	now   func() time.Time
}

func (m *gatewayModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if until := m.pause.ModelPausedUntil(m.now()); !until.IsZero() {
			yield(nil, &ModelCallError{Err: errModelPaused, Paused: true, Until: until})
			return
		}
		for resp, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if err == nil && !resp.Partial {
				err = checkResponse(resp)
			}
			if err != nil {
				yield(nil, m.failed(err))
				return
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}

func (m *gatewayModel) failed(err error) error {
	if errors.Is(err, context.Canceled) {
		// The step was stopped; the call did not fail.
		return err
	}
	err = gatewayError(err)
	until := m.pause.NoteModelError(err, m.now())
	return &ModelCallError{Err: err, Paused: !until.IsZero(), Until: until}
}

// gatewayError maps the client's errors onto the loop's model errors.
func gatewayError(err error) error {
	var apiErr *openai.Error
	switch {
	case errors.As(err, &apiErr):
		httpErr := &apmodel.HTTPError{StatusCode: apiErr.StatusCode}
		if apiErr.Response != nil {
			httpErr.Body = errorBody(apiErr.Response)
			httpErr.RetryAfter = apmodel.RetryAfter(apiErr.Response.Header)
		}
		return httpErr
	case errors.Is(err, openaimodel.ErrNoChoices),
		errors.Is(err, openaimodel.ErrNoTextOrToolContent),
		errors.Is(err, openaimodel.ErrEmptyResponse):
		return fmt.Errorf("%w: %w", apmodel.ErrInvalidResponse, err)
	}
	return err
}

// errorBody reads the start of a refused call's body. openai-go keeps the
// whole body there and decodes only its "error" object, which can leave out
// what the loop's classifier looks for.
func errorBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return string(b)
}

// checkResponse refuses a final response the loop would not use: one with no
// text, or one the model did not finish.
func checkResponse(resp *model.LLMResponse) error {
	var text strings.Builder
	if resp.Content != nil {
		for _, p := range resp.Content.Parts {
			if p != nil && !p.Thought {
				text.WriteString(p.Text)
			}
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return fmt.Errorf("%w: empty content", apmodel.ErrInvalidResponse)
	}
	if resp.FinishReason != genai.FinishReasonStop {
		return fmt.Errorf("%w: finish reason %s", apmodel.ErrInvalidResponse, resp.FinishReason)
	}
	return nil
}

// newAnalyzeAgent is the analyze node's agent: one model call with the
// request gather built as the only message. The reply comes back as text and
// record validates it, so a bad reply is a route, not a node failure.
func newAnalyzeAgent(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "analyze",
		Description: "Answers the question from call-graph evidence and may recommend probes.",
		Model:       llm,
		// Instruction would substitute the prompt's {...} from session state.
		InstructionProvider: func(agent.ReadonlyContext) (string, error) { return Prompt, nil },
		IncludeContents:     llmagent.IncludeContentsNone,
		GenerateContentConfig: &genai.GenerateContentConfig{
			Temperature:        genai.Ptr[float32](0),
			ResponseJsonSchema: replySchema(),
			HTTPOptions:        &genai.HTTPOptions{Timeout: genai.Ptr(modelTimeout)},
		},
	})
}
