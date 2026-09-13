import { trace } from '@opentelemetry/api';
import {
  AggregationTemporality,
  InMemoryMetricExporter,
  PeriodicExportingMetricReader,
} from '@opentelemetry/sdk-metrics';
import { InMemorySpanExporter } from '@opentelemetry/sdk-trace-web';
import { afterEach, describe, expect, it } from 'vitest';

import { startTelemetry } from './telemetry';

// Hermetic: in-memory exporters, no global registration, no fetch patching.
const testOptions = { instrumentFetch: false, register: false, serviceName: 'unit' };

describe('telemetry', () => {
  // Safety net: startTelemetry() returns the running instance, so a test that
  // failed before its own shutdown() cannot leak state into the next test.
  afterEach(async () => {
    await startTelemetry(testOptions).shutdown();
  });

  it('exports a span through the injected span exporter', async () => {
    const exporter = new InMemorySpanExporter();
    const telemetry = startTelemetry({ ...testOptions, spanExporter: exporter });

    telemetry.tracer.startSpan('unit').end();

    expect(exporter.getFinishedSpans().map((span) => span.name)).toEqual(['unit']);
    await telemetry.shutdown();
  });

  it('collects a counter through the injected metric reader', async () => {
    const metricExporter = new InMemoryMetricExporter(AggregationTemporality.CUMULATIVE);
    const reader = new PeriodicExportingMetricReader({
      exporter: metricExporter,
      exportIntervalMillis: 60_000,
    });
    const telemetry = startTelemetry({ ...testOptions, metricReader: reader });

    telemetry.meter.createCounter('unit.counter').add(1);
    await reader.forceFlush();

    const names = metricExporter
      .getMetrics()
      .flatMap((resourceMetrics) => resourceMetrics.scopeMetrics)
      .flatMap((scopeMetrics) => scopeMetrics.metrics)
      .map((metric) => metric.descriptor.name);
    expect(names).toContain('unit.counter');
    await telemetry.shutdown();
  });

  it('is idempotent until shutdown', async () => {
    const first = startTelemetry(testOptions);

    expect(startTelemetry(testOptions)).toBe(first);
    await first.shutdown();

    const second = startTelemetry(testOptions);
    expect(second).not.toBe(first);
    await second.shutdown();
  });

  it('restarts with fresh global providers after shutdown', async () => {
    const globalOptions = { instrumentFetch: false, register: true };
    const exporterA = new InMemorySpanExporter();
    const exporterB = new InMemorySpanExporter();

    const first = startTelemetry({
      ...globalOptions,
      spanExporter: exporterA,
      serviceName: 'restart',
    });
    trace.getTracer('t').startSpan('first').end();
    // Assert before shutdown(): InMemorySpanExporter drops its spans on shutdown.
    expect(exporterA.getFinishedSpans().map((span) => span.name)).toEqual(['first']);
    await first.shutdown();

    const second = startTelemetry({ ...globalOptions, spanExporter: exporterB });
    trace.getTracer('t').startSpan('second').end();
    expect(exporterB.getFinishedSpans().map((span) => span.name)).toEqual(['second']);
    await second.shutdown();
  });
});
