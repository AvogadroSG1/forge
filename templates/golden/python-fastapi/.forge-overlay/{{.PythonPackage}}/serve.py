"""Process entry point: the ``app`` console script.

Binds to ``HOST``/``PORT`` from the environment (defaults ``127.0.0.1:8000``)
and writes one JSON object per log line to stdout, uvicorn's own logs included.
"""

import json
import logging
import os
from typing import Any

import uvicorn

from .main import app


class JSONFormatter(logging.Formatter):
    """Render each record as a single-line JSON object."""

    def format(self, record: logging.LogRecord) -> str:
        entry: dict[str, Any] = {
            "time": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        if record.exc_info:
            entry["exception"] = self.formatException(record.exc_info)
        return json.dumps(entry)


_HANDLER = {
    "class": "logging.StreamHandler",
    "formatter": "json",
    "stream": "ext://sys.stdout",
}

LOG_CONFIG: dict[str, Any] = {
    "version": 1,
    "disable_existing_loggers": False,
    "formatters": {"json": {"()": f"{__name__}.JSONFormatter"}},
    "handlers": {"stdout": _HANDLER},
    "loggers": {
        "uvicorn": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
        "uvicorn.error": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
        "uvicorn.access": {"handlers": ["stdout"], "level": "INFO", "propagate": False},
    },
    "root": {"handlers": ["stdout"], "level": "INFO"},
}


def main() -> None:
    uvicorn.run(
        app,
        host=os.environ.get("HOST", "127.0.0.1"),
        port=int(os.environ.get("PORT", "8000")),
        log_config=LOG_CONFIG,
    )
