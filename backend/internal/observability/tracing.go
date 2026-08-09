package observability

import (
	"context"
	"errors"

	"basisvr-social-service/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func InitTracing(ctx context.Context, cfg config.ObservabilityConfig) (func(context.Context) error, error) {
	if !cfg.TracingEnabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.TracingSampleRatio < 0 || cfg.TracingSampleRatio > 1 {
		return nil, errors.New("OTEL_TRACES_SAMPLER_RATIO must be between 0 and 1")
	}
	options := []otlptracehttp.Option{}
	if cfg.TracingEndpoint != "" {
		options = append(options, otlptracehttp.WithEndpointURL(cfg.TracingEndpoint))
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(cfg.TracingService)),
	)
	if err != nil {
		return nil, err
	}
	provider := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
		trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(cfg.TracingSampleRatio))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return provider.Shutdown, nil
}
