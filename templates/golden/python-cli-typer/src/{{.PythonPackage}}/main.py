import typer

from .telemetry import configure, get_tracer

configure()

app = typer.Typer(no_args_is_help=True)


@app.command()
def hello(name: str = "world") -> None:
    with get_tracer().start_as_current_span("hello"):
        print(f"hello, {name}!")


def main() -> None:
    app()


if __name__ == "__main__":
    main()
