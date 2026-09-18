#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import re
import sys

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[1]
APP = ROOT / "influxdb_mcp"


def fail(message: str) -> None:
    print(f"validation error: {message}", file=sys.stderr)
    raise SystemExit(1)


def load_yaml(path: pathlib.Path) -> dict:
    try:
        value = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        fail(f"cannot parse {path.relative_to(ROOT)}: {exc}")
    if not isinstance(value, dict):
        fail(f"{path.relative_to(ROOT)} must contain a mapping")
    return value


def main() -> None:
    required_files = [
        ROOT / "repository.yaml",
        APP / "config.yaml",
        APP / "Dockerfile",
        APP / "DOCS.md",
        APP / "CHANGELOG.md",
        APP / "translations" / "en.yaml",
        APP / "go.mod",
        APP / "go.sum",
    ]
    missing = [str(path.relative_to(ROOT)) for path in required_files if not path.is_file()]
    if missing:
        fail("missing required files: " + ", ".join(missing))

    repository = load_yaml(ROOT / "repository.yaml")
    if not repository.get("name"):
        fail("repository.yaml needs a non-empty name")

    config = load_yaml(APP / "config.yaml")
    required_keys = {"name", "version", "slug", "description", "arch", "options", "schema"}
    absent = sorted(required_keys - config.keys())
    if absent:
        fail("config.yaml is missing: " + ", ".join(absent))
    if config["slug"] != "influxdb_mcp":
        fail("config.yaml slug must be influxdb_mcp")
    if not re.fullmatch(r"\d+\.\d+\.\d+", str(config["version"])):
        fail("config.yaml version must be semantic x.y.z")
    if set(config["arch"]) != {"amd64", "aarch64"}:
        fail("arch must contain exactly amd64 and aarch64")
    if config.get("init") is not True:
        fail("init must be true because the runtime image has no init system")
    if config.get("ports", {}).get("8080/tcp", "missing") is not None:
        fail("port 8080 must be unmapped by default")

    options = config["options"]
    schema = config["schema"]
    extra_options = sorted(set(options) - set(schema))
    if extra_options:
        fail("options missing from schema: " + ", ".join(extra_options))
    for secret in ("influx_password", "openai_tunnel_api_key"):
        if not str(schema.get(secret, "")).startswith("password"):
            fail(f"{secret} must use the password schema type")

    changelog = (APP / "CHANGELOG.md").read_text(encoding="utf-8")
    if f"## {config['version']}" not in changelog:
        fail("CHANGELOG.md has no heading for config.yaml version")

    dockerfile = (APP / "Dockerfile").read_text(encoding="utf-8")
    for label in ("io.hass.version", "io.hass.type", "io.hass.arch"):
        if label not in dockerfile:
            fail(f"Dockerfile is missing {label} label")
    if "golang:1.27.0" not in dockerfile:
        fail("Dockerfile Go version must match the go.mod 1.27 toolchain")

    print("repository validation passed")


if __name__ == "__main__":
    main()
