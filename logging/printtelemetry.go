package logging

import (
	"encoding/json"
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func EzMarshal[T any](content T) string {
	telem, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return fmt.Sprintf("Cannot print telemetry: %v", err)
	} else {
		return string(telem)
	}
}

func EzPrint[T any](content T) {
	telem, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		fmt.Printf("Call stack:\n%s\n", debug.Stack())
		fmt.Printf("Cannot print telemetry: %v", err)
	} else {
		fmt.Println(string(telem))
	}
}

func EzFail(span trace.Span, err error) {
	EzPrint(err)
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
	span.SetAttributes(attribute.String("error_details", EzMarshal(err)))
}
