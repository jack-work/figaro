package anthropicbridge

import (
	"context"
	"errors"
	"figaro/mcp"
	"figaro/utils"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"go.opentelemetry.io/otel/trace"
)

type AnthropicBridge struct {
	tracerProvider trace.TracerProvider
	client         *anthropic.Client
}

type Opts struct {
	tracerProvider trace.TracerProvider
}

type OptsFunc func(o *Opts)

func WithLogging(tp trace.TracerProvider) OptsFunc {
	return func(o *Opts) {
		o.tracerProvider = tp
	}
}

func InitAnthropic(opts ...OptsFunc) (AnthropicBridge, error) {
	o := Opts{
		tracerProvider: nil,
	}
	for _, optFunc := range opts {
		optFunc(&o)
	}

	client, err := GetAnthropicClient()
	if err != nil {
		return AnthropicBridge{}, err
	}

	return AnthropicBridge{
		tracerProvider: o.tracerProvider,
		client:         client,
	}, nil
}

func GetAnthropicTools(tools []mcp.Tool) []anthropic.ToolUnionParam {
	return utils.Map2(tools, getAnthropicTool)
}

func getAnthropicTool(val mcp.Tool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{
		OfTool: &anthropic.ToolParam{
			Name:        val.Name,
			InputSchema: getAnthropicInputSchema(val.InputSchema),
			Description: param.Opt[string]{
				Value: *val.Description,
			},
		},
	}
}

func getAnthropicInputSchema(schema mcp.ToolInputSchema) anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{
		Properties: schema.Properties,
		Type:       constant.Object(schema.Type),
	}
}

func GetAnthropicClient() (*anthropic.Client, error) {
	apiKey, ok := os.LookupEnv("ANTHROPIC_API_KEY")
	if !ok {
		return nil, errors.New("No ANTHROPIC_API_KEY found")
	}
	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)
	return &client, nil
}

type ConsoleStreamable[T any] struct {
	Progress <-chan string
	Result   <-chan T
	Error    <-chan error
}

// newer version that uses built in anthropic types
func (bridge *AnthropicBridge) StreamMessage(
	ctx context.Context,
	input anthropic.MessageNewParams,
) (
	*ConsoleStreamable[*anthropic.Message],
	error,
) {
	ctx, cancel := context.WithCancelCause(ctx)
	tracer := bridge.tracerProvider.Tracer("anthropicbridge")
	ctx, span := tracer.Start(ctx, "StreamMessage")

	client := bridge.client
	stream := client.Messages.NewStreaming(ctx, input)

	err := stream.Err()
	if err != nil {
		cancel(err)
		span.End(trace.WithStackTrace(true), trace.WithTimestamp(time.Now()))
		return nil, stream.Err()
	}

	errCh := make(chan error)
	message := anthropic.Message{}
	// TODO: Move the stream processing logic from figaro to the anthropic bridge.
	// Then, define an interface for the bridges and make it easier to include other
	// models.
	progress := make(chan string, 1)
	result := make(chan *anthropic.Message)

	go func() {
		defer close(errCh)
		select {
		case <-ctx.Done():
			err := ctx.Err()
			if err != nil {
				errCh <- err
			}
		}
	}()

	go func() {
		defer close(progress)
		defer close(result)
		defer span.End()
		defer cancel(nil)
		for stream.Next() {
			event := stream.Current()
			err := message.Accumulate(event)
			if err != nil {
				cancel(err)
			}

			switch variant := event.AsAny().(type) {
			case anthropic.ContentBlockDeltaEvent:
				switch deltaVariant := variant.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					progress <- deltaVariant.Text
				default:
					// progress <- reflect.TypeOf(deltaVariant).Name() + "\n"
				}
			}
		}
		result <- &message
	}()

	return &ConsoleStreamable[*anthropic.Message]{
		Progress: progress,
		Result:   result,
	}, nil
}
