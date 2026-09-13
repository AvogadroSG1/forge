package web

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestIndexPageRendersTheSkeleton(t *testing.T) {
	router := NewServer("Sample App", log.New(io.Discard, "", 0))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "API health") {
		t.Fatalf("body missing health copy:\n%s", body)
	}
	if !strings.Contains(body, "Sample App") {
		t.Fatalf("body missing project title:\n%s", body)
	}
	if !strings.Contains(body, "/static/htmx.min.js") {
		t.Fatalf("body missing local htmx script:\n%s", body)
	}
}

func TestHealthEndpointReturnsOK(t *testing.T) {
	router := NewServer("Sample App", log.New(io.Discard, "", 0))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if diff := cmp.Diff(`{"status":"ok"}`, response.Body.String()); diff != "" {
		t.Fatalf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestHealthRequestRecordsServerSpan(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(ctx)
	})

	router := NewServer("Sample App", log.New(io.Discard, "", 0))
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	if spans[0].Name != "GET /health" {
		t.Fatalf("span name = %q, want %q", spans[0].Name, "GET /health")
	}
}

func TestStaticHTMXIsServedLocally(t *testing.T) {
	router := NewServer("Sample App", log.New(io.Discard, "", 0))

	request := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}
