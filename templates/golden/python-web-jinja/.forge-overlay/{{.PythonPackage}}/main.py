from pathlib import Path

from fastapi import FastAPI, Request, Response
from fastapi.responses import HTMLResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor

from .telemetry import configure

APP_DIR = Path(__file__).resolve().parent

configure()

app = FastAPI()
# Under `opentelemetry-instrument` the FastAPI class is already patched and the
# app arrives instrumented; only instrument by hand when that did not happen.
if not getattr(app, "_is_instrumented_by_opentelemetry", False):
    FastAPIInstrumentor.instrument_app(app)
app.mount("/static", StaticFiles(directory=APP_DIR / "static"), name="static")
templates = Jinja2Templates(directory=APP_DIR / "templates")


@app.get("/", response_class=HTMLResponse)
def index(request: Request) -> Response:
    return templates.TemplateResponse(
        request=request, name="index.html", context={"status": "checking…"}
    )


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/health/fragment", response_class=HTMLResponse)
def health_fragment() -> HTMLResponse:
    return HTMLResponse("ok")
