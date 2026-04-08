"""
cc.py — Thin Python client for the cc-sdk binary.

Drop this single file into any Python project to use the Cloud Compute SDK.
No pip install, no dependencies beyond the standard library.

Usage:
    from cc import CcSdk

    with CcSdk() as sdk:
        payload = sdk.get_payload()

        # Dot-access on payload objects:
        payload.attributes["catalog_id"]
        payload.inputs[0].paths["watershed"]
        payload.inputs[0].name
        for action in payload.actions:
            print(action.name)

        sdk.copy_to_local("ds", "key", "/tmp/file.txt")
        sdk.copy_to_remote("ds", "key", "/tmp/file.txt")

One-shot (no persistent process, simpler but slower for multiple calls):
    from cc import get_payload, copy_to_local, copy_to_remote
    payload = get_payload()
    copy_to_local("ds", "key", "/tmp/file.txt")
"""

from __future__ import annotations

import atexit
import base64
import io
import json
import logging
import os
import subprocess
import threading
from typing import Any, Optional

__version__ = "2.0.0"

log = logging.getLogger("cc")

CC_SDK_BIN_ENV = "CC_SDK_BIN"


def _bin() -> str:
    """Find the cc-sdk binary: env var > bundled in package > PATH."""
    # 1. Explicit override
    env_bin = os.environ.get(CC_SDK_BIN_ENV)
    if env_bin:
        return env_bin

    # 2. Bundled binary next to this file (inside the installed wheel)
    import sys
    pkg_dir = os.path.dirname(os.path.abspath(__file__))
    name = "cc-sdk.exe" if sys.platform == "win32" else "cc-sdk"
    bundled = os.path.join(pkg_dir, name)
    if os.path.isfile(bundled):
        return bundled

    # 3. Fall back to PATH
    return "cc-sdk"


class CcSdkError(Exception):
    """Raised when cc-sdk returns an error."""


# ---------------------------------------------------------------------------
# Payload wrapper — idiomatic dot-access over raw JSON dicts
# ---------------------------------------------------------------------------


class _DotDict:
    """Read-only dot-access wrapper around a dict.

    Allows ``obj.key`` instead of ``obj["key"]``.  Nested dicts become
    nested _DotDict instances.  Lists of dicts become lists of _DotDict.
    Unknown attribute access raises AttributeError with a helpful message.
    The underlying dict is still accessible for iteration and key lookup.
    """

    __slots__ = ("_data",)

    def __init__(self, data: dict):
        object.__setattr__(self, "_data", data)

    def __getattr__(self, key: str):
        try:
            return _wrap(self._data[key])
        except KeyError:
            raise AttributeError(
                f"No attribute {key!r}. Available: {list(self._data)}"
            )

    def __getitem__(self, key: str):
        return _wrap(self._data[key])

    def __contains__(self, key: str) -> bool:
        return key in self._data

    def __iter__(self):
        return iter(self._data)

    def __len__(self) -> int:
        return len(self._data)

    def __repr__(self) -> str:
        return f"{type(self).__name__}({self._data!r})"

    def get(self, key: str, default=None):
        val = self._data.get(key, default)
        return _wrap(val) if val is not default else default

    def keys(self):
        return self._data.keys()

    def values(self):
        return [_wrap(v) for v in self._data.values()]

    def items(self):
        return [(k, _wrap(v)) for k, v in self._data.items()]

    def to_dict(self) -> dict:
        """Return the underlying raw dict."""
        return self._data


def _wrap(val):
    """Recursively wrap dicts and lists of dicts."""
    if isinstance(val, dict):
        return _DotDict(val)
    if isinstance(val, list):
        return [_wrap(v) for v in val]
    return val


class StoreCredentials:
    """Resolved credentials for a CC data store profile."""

    __slots__ = ("aws_access_key_id", "aws_secret_access_key", "region",
                 "bucket", "endpoint", "profile")

    def __init__(self, profile: str):
        self.profile = profile
        self.aws_access_key_id = os.environ.get(f"{profile}_AWS_ACCESS_KEY_ID", "")
        self.aws_secret_access_key = os.environ.get(f"{profile}_AWS_SECRET_ACCESS_KEY", "")
        self.region = os.environ.get(f"{profile}_AWS_DEFAULT_REGION", "")
        self.bucket = os.environ.get(f"{profile}_AWS_S3_BUCKET", "")
        self.endpoint = os.environ.get(f"{profile}_AWS_ENDPOINT", "")

    def s3_uri(self, root: str, suffix: str = "") -> str:
        """Build an S3 URI from bucket + root + optional suffix."""
        parts = f"s3://{self.bucket}/{root}"
        if suffix:
            parts = f"{parts}/{suffix}"
        return parts

    def tiledb_config(self) -> dict:
        """Return a dict suitable for tiledb.Config()."""
        cfg = {
            "vfs.s3.aws_access_key_id": self.aws_access_key_id,
            "vfs.s3.aws_secret_access_key": self.aws_secret_access_key,
            "vfs.s3.region": self.region,
        }
        if self.endpoint:
            # Strip protocol for TileDB endpoint_override
            ep = self.endpoint
            scheme = "https"
            if "://" in ep:
                scheme, ep = ep.split("://", 1)
            cfg["vfs.s3.scheme"] = scheme
            cfg["vfs.s3.endpoint_override"] = ep
            cfg["vfs.s3.use_virtual_addressing"] = "false"
        return cfg

    def boto3_session_kwargs(self) -> dict:
        """Return kwargs suitable for boto3.Session()."""
        return {
            "aws_access_key_id": self.aws_access_key_id,
            "aws_secret_access_key": self.aws_secret_access_key,
            "region_name": self.region,
        }


class Payload(_DotDict):
    """Typed convenience accessors for the CC payload.

    All fields are also accessible via dot-notation on the underlying dict,
    so ``payload.attributes["key"]`` and ``payload["attributes"]["key"]``
    both work.
    """

    @property
    def attributes(self) -> _DotDict:
        return _wrap(self._data.get("attributes", {}))

    @property
    def stores(self) -> list:
        return _wrap(self._data.get("stores", []))

    @property
    def inputs(self) -> list:
        return _wrap(self._data.get("inputs", []))

    @property
    def outputs(self) -> list:
        return _wrap(self._data.get("outputs", []))

    @property
    def actions(self) -> list:
        return _wrap(self._data.get("actions", []))

    def get_store(self, name: str) -> _DotDict:
        """Find a store by name."""
        for s in self.stores:
            if s.name == name:
                return s
        raise KeyError(f"Store {name!r} not found. Available: {[s.name for s in self.stores]}")

    def get_store_credentials(self, store_name: str) -> StoreCredentials:
        """Resolve credentials for a named store using its CC profile."""
        store = self.get_store(store_name)
        return StoreCredentials(store.profile)



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
        # Wait for ready signal
        ready_line = self._proc.stdout.readline()
        if not ready_line:
            stderr = self._proc.stderr.read().decode("utf-8", errors="replace")
            raise CcSdkError(f"cc-sdk failed to start: {stderr}")
        ready = json.loads(ready_line)
        if not ready.get("ok") or ready.get("cmd") != "ready":
            raise CcSdkError(f"cc-sdk unexpected ready signal: {ready}")
        log.debug("cc-sdk serve ready (pid=%d)", self._proc.pid)
        atexit.register(self.shutdown)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.shutdown()

    def _next_id(self) -> str:
        self._counter += 1
        return str(self._counter)

    def _request(self, cmd: str, **kwargs: Any) -> dict:
        """Send a JSON-line request and read the response."""
        if self._proc.poll() is not None:
            stderr = self._proc.stderr.read().decode("utf-8", errors="replace")
            raise CcSdkError(f"cc-sdk process exited unexpectedly: {stderr}")

        req = {"id": self._next_id(), "cmd": cmd}
        # Only include non-empty optional fields
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

    def get_payload(self) -> Payload:
        """Fetch the payload with dot-access on all fields."""
        resp = self._request("get-payload")
        return Payload(resp["data"])

    def copy_to_local(
        self,
        ds_name: str,
        pathkey: str,
        localpath: str,
        datakey: str = "",
    ) -> None:
        """Download a remote object to a local file."""
        self._request(
            "copy-to-local",
            ds_name=ds_name,
            pathkey=pathkey,
            datakey=datakey,
            localpath=localpath,
        )

    def copy_to_remote(
        self,
        ds_name: str,
        pathkey: str,
        localpath: str,
        datakey: str = "",
    ) -> None:
        """Upload a local file to the remote store."""
        self._request(
            "copy-to-remote",
            ds_name=ds_name,
            pathkey=pathkey,
            datakey=datakey,
            localpath=localpath,
        )

    def copy_folder_to_remote(
        self,
        ds_name: str,
        pathkey: str,
        localpath: str,
        datakey: str = "",
    ) -> None:
        """Recursively upload a local directory."""
        self._request(
            "copy-folder-to-remote",
            ds_name=ds_name,
            pathkey=pathkey,
            datakey=datakey,
            localpath=localpath,
        )

    def get(self, ds_name: str, pathkey: str, datakey: str = "") -> bytes:
        """Fetch remote object as bytes."""
        resp = self._request("get", ds_name=ds_name, pathkey=pathkey, datakey=datakey)
        return base64.b64decode(resp["data_b64"])

    def get_reader(
        self, ds_name: str, pathkey: str, datakey: str = ""
    ) -> io.BytesIO:
        """Fetch remote object as a file-like reader."""
        return io.BytesIO(self.get(ds_name, pathkey, datakey))

    def put(
        self,
        ds_name: str,
        pathkey: str,
        data: bytes | io.IOBase,
        datakey: str = "",
    ) -> None:
        """Upload bytes to a remote object."""
        if hasattr(data, "read"):
            data = data.read()
        self._request(
            "put",
            ds_name=ds_name,
            pathkey=pathkey,
            datakey=datakey,
            data_b64=base64.b64encode(data).decode("ascii"),
        )

    def copy(
        self,
        src_ds: str,
        src_pathkey: str,
        dst_ds: str,
        dst_pathkey: str,
        src_datakey: str = "",
        dst_datakey: str = "",
    ) -> None:
        """Copy between data sources."""
        self._request(
            "copy",
            src_ds=src_ds,
            src_pathkey=src_pathkey,
            src_datakey=src_datakey,
            dst_ds=dst_ds,
            dst_pathkey=dst_pathkey,
            dst_datakey=dst_datakey,
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


# ---------------------------------------------------------------------------
# One-shot convenience functions (no persistent process)
# ---------------------------------------------------------------------------


def _run(args: list[str], input_data: bytes | None = None) -> subprocess.CompletedProcess:
    """Run a one-shot cc-sdk command."""
    cmd = [_bin()] + args
    try:
        result = subprocess.run(cmd, input=input_data, capture_output=True, check=False)
    except FileNotFoundError:
        raise CcSdkError(
            f"'{_bin()}' not found. Install cc-sdk or set {CC_SDK_BIN_ENV}."
        )
    if result.returncode != 0:
        stderr = result.stderr.decode("utf-8", errors="replace").strip()
        raise CcSdkError(f"cc-sdk {' '.join(args)} failed: {stderr}")
    return result


def get_payload() -> Payload:
    """One-shot: fetch the payload with dot-access on all fields."""
    result = _run(["get-payload"])
    return Payload(json.loads(result.stdout))


def copy_to_local(
    ds_name: str, pathkey: str, localpath: str, datakey: str = ""
) -> None:
    """One-shot: download a remote object to a local file."""
    args = ["copy-to-local", ds_name, pathkey]
    if datakey:
        args.append(datakey)
    args.append(localpath)
    _run(args)


def copy_to_remote(
    ds_name: str, pathkey: str, localpath: str, datakey: str = ""
) -> None:
    """One-shot: upload a local file to the remote store."""
    args = ["copy-to-remote", ds_name, pathkey]
    if datakey:
        args.append(datakey)
    args.append(localpath)
    _run(args)
