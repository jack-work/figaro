package otel

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var configuredOTLP string

func SetOTLPEndpoint(endpoint string) { configuredOTLP = strings.TrimSpace(endpoint) }

const (
	otlpTimeout  = 5 * time.Second
	otlpLogQueue = 4096
	otlpInterval = 15 * time.Second
)

type otlpSinks struct {
	spans   sdktrace.SpanProcessor
	logs    sdklog.Processor
	metrics sdkmetric.Reader
}

func otlpBase(endpoint string) (string, error) {
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("otlp_endpoint scheme %q: want http or https", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("otlp_endpoint %q has no host", endpoint)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func newOTLPSinks(ctx context.Context, endpoint string) (*otlpSinks, error) {
	base, err := otlpBase(endpoint)
	if err != nil {
		return nil, err
	}
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(base+"/v1/traces"),
		otlptracehttp.WithTimeout(otlpTimeout))
	if err != nil {
		return nil, fmt.Errorf("otlp traces: %w", err)
	}
	logExp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(base+"/v1/logs"),
		otlploghttp.WithTimeout(otlpTimeout))
	if err != nil {
		return nil, fmt.Errorf("otlp logs: %w", err)
	}
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"),
		otlpmetrichttp.WithTimeout(otlpTimeout),
		otlpmetrichttp.WithTemporalitySelector(func(k sdkmetric.InstrumentKind) metricdata.Temporality {
			switch k {
			case sdkmetric.InstrumentKindUpDownCounter, sdkmetric.InstrumentKindObservableUpDownCounter, sdkmetric.InstrumentKindObservableGauge:
				return metricdata.CumulativeTemporality
			}
			return metricdata.DeltaTemporality
		}))
	if err != nil {
		return nil, fmt.Errorf("otlp metrics: %w", err)
	}
	return &otlpSinks{
		spans:   sdktrace.NewBatchSpanProcessor(traceExp, sdktrace.WithExportTimeout(otlpTimeout)),
		logs:    newBoundedProcessor(logExp, otlpLogQueue),
		metrics: sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(otlpInterval), sdkmetric.WithTimeout(otlpTimeout)),
	}, nil
}
