"""
cc.py — Thin Python client for the cc-sdk binary.

Drop this single file into any Python project to use the Cloud Compute SDK.
No external dependencies beyond the standard library.

    from cc import CcSdk

    with CcSdk() as sdk:
        payload = sdk.get_payload()

        # Typed dataclass access
        payload.attributes["catalog_id"]
        payload.inputs[0].paths["watershed"]
        payload.inputs[0].name

        sdk.copy_to_local("ds", "key", "", "/tmp/file.txt")
        sdk.copy_to_remote("ds", "key", "", "/tmp/file.txt")
"""

from __future__ import annotations

import atexit
import base64
import io
import json
import logging
import os
import subprocess
import sys
import threading
from dataclasses import dataclass
from typing import Any, Optional

__version__ = "2.0.0"

log = logging.getLogger("cc")

CC_SDK_BIN_ENV = "CC_SDK_BIN"


def _bin() -> str:
    """Find the cc-sdk binary: $CC_SDK_BIN > bundled-in-package > PATH."""
    env_bin = os.environ.get(CC_SDK_BIN_ENV)
    if env_bin:
        return env_bin

    pkg_dir = os.path.dirname(os.path.abspath(__file__))
    name = "cc-sdk.exe" if sys.platform == "win32" else "cc-sdk"
    bundled = os.path.join(pkg_dir, name)
    if os.path.isfile(bundled):
        return bundled

    return "cc-sdk"


class CcSdkError(Exception):
    """Raised when cc-sdk returns an error."""


# ---------------------------------------------------------------------------
# Payload types — dataclasses mirror the Go CLI's JSON schema
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class DataSource:
    """Typed view of a CC data source (input or output)."""
    name: str
    id: str
    store_name: str
    paths: dict[str, str]
    data_paths: dict[str, str]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> DataSource:
        return cls(
            name=d.get("name", ""),
            id=d.get("id", ""),
            store_name=d.get("store_name", ""),
            paths=dict(d.get("paths") or {}),
            data_paths=dict(d.get("data_paths") or {}),
        )


@dataclass(frozen=True)
class Store:
    """Typed view of a CC data store."""
    name: str
    id: str
    store_type: str
    profile: str
    params: dict[str, Any]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> Store:
        return cls(
            name=d.get("name", ""),
            id=d.get("id", ""),
            store_type=d.get("store_type", ""),
            profile=d.get("profile", ""),
            params=dict(d.get("params") or {}),
        )


@dataclass(frozen=True)
class Action:
    """Typed view of a CC action."""
    name: str
    type: str
    description: str
    attributes: dict[str, Any]
    stores: list[Store]
    inputs: list[DataSource]
    outputs: list[DataSource]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> Action:
        return cls(
            name=d.get("name", ""),
            type=d.get("type", ""),
            description=d.get("description", ""),
            attributes=dict(d.get("attributes") or {}),
            stores=[Store.from_dict(s) for s in (d.get("stores") or [])],
            inputs=[DataSource.from_dict(i) for i in (d.get("inputs") or [])],
            outputs=[DataSource.from_dict(o) for o in (d.get("outputs") or [])],
        )


@dataclass(frozen=True)
class StoreCredentials:
    """Resolved credentials for a CC data store profile."""
    profile: str
    aws_access_key_id: str
    aws_secret_access_key: str
    region: str
    bucket: str
    endpoint: str

    @classmethod
    def from_env(cls, profile: str) -> StoreCredentials:
        return cls(
            profile=profile,
            aws_access_key_id=os.environ.get(f"{profile}_AWS_ACCESS_KEY_ID", ""),
            aws_secret_access_key=os.environ.get(f"{profile}_AWS_SECRET_ACCESS_KEY", ""),
            region=os.environ.get(f"{profile}_AWS_DEFAULT_REGION", ""),
            bucket=os.environ.get(f"{profile}_AWS_S3_BUCKET", ""),
            endpoint=os.environ.get(f"{profile}_AWS_ENDPOINT", ""),
        )

    def s3_uri(self, root: str, suffix: str = "") -> str:
        """Build an S3 URI from bucket + root + optional suffix."""
        base = f"s3://{self.bucket}/{root}"
        return f"{base}/{suffix}" if suffix else base

    def tiledb_config(self) -> dict[str, str]:
        """Return a dict suitable for tiledb.Config()."""
        cfg = {
            "vfs.s3.aws_access_key_id": self.aws_access_key_id,
            "vfs.s3.aws_secret_access_key": self.aws_secret_access_key,
            "vfs.s3.region": self.region,
        }
        if self.endpoint:
            ep = self.endpoint
            scheme = "https"
            if "://" in ep:
                scheme, ep = ep.split("://", 1)
            cfg["vfs.s3.scheme"] = scheme
            cfg["vfs.s3.endpoint_override"] = ep
            cfg["vfs.s3.use_virtual_addressing"] = "false"
        return cfg

    def boto3_session_kwargs(self) -> dict[str, str]:
        """Return kwargs suitable for boto3.Session()."""
        return {
            "aws_access_key_id": self.aws_access_key_id,
            "aws_secret_access_key": self.aws_secret_access_key,
            "region_name": self.region,
        }


@dataclass(frozen=True)
class Payload:
    """Typed view of the full CC payload."""
    attributes: dict[str, Any]
    stores: list[Store]
    inputs: list[DataSource]
    outputs: list[DataSource]
    actions: list[Action]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> Payload:
        return cls(
            attributes=dict(d.get("attributes") or {}),
            stores=[Store.from_dict(s) for s in (d.get("stores") or [])],
            inputs=[DataSource.from_dict(i) for i in (d.get("inputs") or [])],
            outputs=[DataSource.from_dict(o) for o in (d.get("outputs") or [])],
            actions=[Action.from_dict(a) for a in (d.get("actions") or [])],
        )

    def get_store(self, name: str) -> Store:
        """Find a store by name."""
        for s in self.stores:
            if s.name == name:
                return s
        raise KeyError(
            f"Store {name!r} not found. Available: {[s.name for s in self.stores]}"
        )

    def get_store_credentials(self, store_name: str) -> StoreCredentials:
        """Resolve credentials for a named store using its CC profile."""
        return StoreCredentials.from_env(self.get_store(store_name).profile)


# ---------------------------------------------------------------------------
# Persistent mode (cc-sdk serve)
# ---------------------------------------------------------------------------


class CcSdk:
    """Manages a long-running cc-sdk serve process."""

    def __init__(self, bin_path: Optional[str] = None):
        self._bin = bin_path or _bin()
        self._lock = threading.Lock()
        self._counter = 0
        self._proc = subprocess.Popen(
            [self._bin, "serve"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

        ready_line = self._proc.stdout.readline()
        if not ready_line:
            stderr = self._proc.stderr.read().decode("utf-8", errors="replace")
            raise CcSdkError(f"cc-sdk failed to start: {stderr}")
        ready = json.loads(ready_line)
        if not ready.get("ok") or ready.get("cmd") != "ready":
            raise CcSdkError(f"cc-sdk unexpected ready signal: {ready}")

        log.debug("cc-sdk serve ready (pid=%d)", self._proc.pid)
        atexit.register(self.shutdown)

    def __enter__(self) -> CcSdk:
        return self

    def __exit__(self, *exc: Any) -> None:
        self.shutdown()

    def _next_id(self) -> str:
        self._counter += 1
        return str(self._counter)

    def _request(self, cmd: str, **kwargs: Any) -> dict[str, Any]:
        """Send a JSON-line request and read the response."""
        if self._proc.poll() is not None:
            stderr = self._proc.stderr.read().decode("utf-8", errors="replace")
            raise CcSdkError(f"cc-sdk process exited unexpectedly: {stderr}")

        req: dict[str, Any] = {"id": self._next_id(), "cmd": cmd}
        for k, v in kwargs.items():
            if v is not None and v != "":
                req[k] = v

        line = json.dumps(req, separators=(",", ":")) + "\n"

        with self._lock:
            self._proc.stdin.write(line.encode())
            self._proc.stdin.flush()
            resp_line = self._proc.stdout.readline()

        if not resp_line:
            stderr = self._proc.stderr.read().decode("utf-8", errors="replace")
            raise CcSdkError(f"cc-sdk returned empty response: {stderr}")

        resp = json.loads(resp_line)
        if not resp.get("ok"):
            raise CcSdkError(resp.get("error", "unknown error"))
        return resp

    # ----- Operations — each maps 1:1 to a cc-sdk serve command -----

    def get_payload(self) -> Payload:
        """Fetch the payload."""
        resp = self._request("get-payload")
        return Payload.from_dict(resp["data"])

    def copy_to_local(
        self, ds_name: str, pathkey: str, datakey: str, localpath: str
    ) -> None:
        """Download a remote object to a local file."""
        self._request(
            "copy-to-local",
            ds_name=ds_name, pathkey=pathkey,
            datakey=datakey, localpath=localpath,
        )

    def copy_to_remote(
        self, ds_name: str, pathkey: str, datakey: str, localpath: str
    ) -> None:
        """Upload a local file to the remote store."""
        self._request(
            "copy-to-remote",
            ds_name=ds_name, pathkey=pathkey,
            datakey=datakey, localpath=localpath,
        )

    def copy_folder_to_remote(
        self, ds_name: str, pathkey: str, datakey: str, localpath: str
    ) -> None:
        """Recursively upload a local directory."""
        self._request(
            "copy-folder-to-remote",
            ds_name=ds_name, pathkey=pathkey,
            datakey=datakey, localpath=localpath,
        )

    def get(self, ds_name: str, pathkey: str, datakey: str) -> bytes:
        """Fetch remote object as bytes."""
        resp = self._request(
            "get", ds_name=ds_name, pathkey=pathkey, datakey=datakey
        )
        return base64.b64decode(resp["data_b64"])

    def get_reader(self, ds_name: str, pathkey: str, datakey: str) -> io.BytesIO:
        """Fetch remote object as a file-like reader."""
        return io.BytesIO(self.get(ds_name, pathkey, datakey))

    def put(
        self, ds_name: str, pathkey: str, datakey: str, data: bytes | io.IOBase
    ) -> None:
        """Upload bytes to a remote object."""
        if hasattr(data, "read"):
            data = data.read()
        self._request(
            "put",
            ds_name=ds_name, pathkey=pathkey, datakey=datakey,
            data_b64=base64.b64encode(data).decode("ascii"),
        )

    def copy(
        self,
        src_ds: str, src_pathkey: str, src_datakey: str,
        dst_ds: str, dst_pathkey: str, dst_datakey: str,
    ) -> None:
        """Copy between data sources."""
        self._request(
            "copy",
            src_ds=src_ds, src_pathkey=src_pathkey, src_datakey=src_datakey,
            dst_ds=dst_ds, dst_pathkey=dst_pathkey, dst_datakey=dst_datakey,
        )

    def shutdown(self) -> None:
        """Shut down the cc-sdk serve process."""
        if self._proc.poll() is not None:
            return
        try:
            with self._lock:
                req = json.dumps({"id": "shutdown", "cmd": "shutdown"}) + "\n"
                self._proc.stdin.write(req.encode())
                self._proc.stdin.flush()
            self._proc.wait(timeout=5)
        except (BrokenPipeError, OSError):
            pass
        except subprocess.TimeoutExpired:
            self._proc.kill()
            self._proc.wait()
        log.debug("cc-sdk serve shut down")
