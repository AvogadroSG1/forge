from fastapi import FastAPI
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor

from .telemetry import configure

configure()

app = FastAPI()
# Under `opentelemetry-instrument` the FastAPI class is already patched and the
# app arrives instrumented; only instrument by hand when that did not happen.
if not getattr(app, "_is_instrumented_by_opentelemetry", False):
    FastAPIInstrumentor.instrument_app(app)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}
