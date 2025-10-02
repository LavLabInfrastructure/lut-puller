import os
import time
import warnings
from pathlib import Path
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Sequence, Tuple, Union
from urllib.parse import urljoin

import requests
import yaml

# Defaults for optional configuration values.
IDP_URL = "https://login.microsoftonline.com"
TOKEN_ENDPOINT = "/oauth2/v2.0/token"

GRANT_TYPE = "client_credentials"

GRAPH_ROOT_URL = "https://graph.microsoft.com"
GRAPH_ROOT_ENDPOINT = "/v1.0"


@dataclass(frozen=True)
class VariableConfig:
    tenant_id: str
    client_id: str
    client_secret: str
    scope: str
    username: str
    password: str
    graph_drive_id: str
    graph_file_id: str
    graph_worksheet_id: str
    key_col: Union[str, int] = "key"
    val_col: Union[str, int] = "val"
    idp_url: str = IDP_URL
    token_endpoint: str = TOKEN_ENDPOINT
    graph_root_url: str = GRAPH_ROOT_URL
    graph_root_endpoint: str = GRAPH_ROOT_ENDPOINT
    output_path: Optional[str] = None


def _normalize_key(key: str) -> str:
    return key.strip().lower().replace("-", "_")


def _load_variable_config(var_name: str, raw_cfg: Any, source: Path) -> VariableConfig:
    if not isinstance(raw_cfg, dict):
        raise RuntimeError(
            f"Configuration for variable '{var_name}' in {source} must be a mapping, got {type(raw_cfg).__name__}."
        )

    normalized: Dict[str, Any] = {_normalize_key(str(k)): v for k, v in raw_cfg.items()}

    def require(field: str) -> str:
        value = normalized.get(field)
        if value is None:
            raise RuntimeError(
                f"Missing required field '{field}' for variable '{var_name}' in {source}."
            )
        if isinstance(value, str):
            value = value.strip()
            if value == "":
                raise RuntimeError(
                    f"Field '{field}' for variable '{var_name}' in {source} cannot be empty."
                )
            return value
        if isinstance(value, (int, float)):
            return str(value)
        raise RuntimeError(
            f"Field '{field}' for variable '{var_name}' in {source} must be a string, got {type(value).__name__}."
        )

    def optional(column_field: str, default: Any) -> Any:
        value = normalized.get(column_field, default)
        if isinstance(value, str):
            stripped = value.strip()
            return default if stripped == "" else stripped
        return value

    allowed_keys = {
        "tenant_id",
        "client_id",
        "client_secret",
        "scope",
        "username",
        "password",
        "graph_drive_id",
        "graph_file_id",
        "graph_worksheet_id",
        "key_col",
        "val_col",
        "idp_url",
        "token_endpoint",
        "graph_root_url",
        "graph_root_endpoint",
        "output_path",
    }
    unknown = set(normalized) - allowed_keys
    if unknown:
        plural = "s" if len(unknown) > 1 else ""
        warnings.warn(
            f"Unknown field{plural} {sorted(unknown)} for variable '{var_name}' in {source}; ignoring."
        )

    return VariableConfig(
        tenant_id=require("tenant_id"),
        client_id=require("client_id"),
        client_secret=require("client_secret"),
        scope=require("scope"),
        username=require("username"),
        password=require("password"),
        graph_drive_id=require("graph_drive_id"),
        graph_file_id=require("graph_file_id"),
        graph_worksheet_id=require("graph_worksheet_id"),
        key_col=optional("key_col", "key"),
        val_col=optional("val_col", "val"),
        idp_url=optional("idp_url", IDP_URL),
        token_endpoint=optional("token_endpoint", TOKEN_ENDPOINT),
        graph_root_url=optional("graph_root_url", GRAPH_ROOT_URL),
        graph_root_endpoint=optional("graph_root_endpoint", GRAPH_ROOT_ENDPOINT),
        output_path=optional("output_path", None),
    )


def load_variable_configs(
    paths: Sequence[Union[str, Path]],
) -> Dict[str, VariableConfig]:
    merged: Dict[str, VariableConfig] = {}
    origins: Dict[str, Path] = {}

    if not paths:
        raise RuntimeError("At least one YAML configuration file must be provided.")

    for p in paths:
        path = Path(p)
        if not path.exists():
            raise FileNotFoundError(f"Configuration file not found: {path}")
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
        if data is None:
            continue
        if not isinstance(data, dict):
            raise RuntimeError(
                f"Top-level YAML structure in {path} must be a mapping of variable names."
            )

        for var_name, raw_cfg in data.items():
            if not isinstance(var_name, str):
                raise RuntimeError(
                    f"Variable name keys in {path} must be strings; got {type(var_name).__name__}."
                )
            config = _load_variable_config(var_name, raw_cfg, path)
            if var_name in merged:
                if merged[var_name] == config:
                    warnings.warn(
                        f"Variable '{var_name}' defined multiple times (e.g. in {origins[var_name]} and {path}) with identical settings; using the first definition."
                    )
                    continue
                raise RuntimeError(
                    f"Conflicting definitions for variable '{var_name}' between {origins[var_name]} and {path}."
                )
            merged[var_name] = config
            origins[var_name] = path

    if not merged:
        raise RuntimeError(
            "No variable configurations found in the provided YAML file(s)."
        )

    return merged


def group_configs_by_output_path(
    configs: Dict[str, VariableConfig],
) -> Dict[str, List[Tuple[str, VariableConfig]]]:
    groups: Dict[str, List[Tuple[str, VariableConfig]]] = {}
    for var_name, cfg in configs.items():
        output_path = cfg.output_path or "./lut.txt"
        groups.setdefault(output_path, []).append((var_name, cfg))
    return groups


class GraphLutPuller:
    """Fetches an Excel worksheet used range via Microsoft Graph and builds a LUT.

    Configuration for a pull operation is provided via ``VariableConfig``.
    Each variable may specify its own Microsoft Graph identifiers, credentials,
    and column selections, allowing a single run to build a LUT spanning
    multiple data sources.
    """

    def __init__(
        self,
        tenant_id: Optional[str] = None,
        client_id: Optional[str] = None,
        client_secret: Optional[str] = None,
        username: Optional[str] = None,
        password: Optional[str] = None,
        scope: Optional[str] = None,
        graph_drive_id: Optional[str] = None,
        graph_file_id: Optional[str] = None,
        graph_worksheet_id: Optional[str] = None,
        key_col: Optional[Union[str, int]] = None,
        val_col: Optional[Union[str, int]] = None,
        idp_url: Optional[str] = None,
        token_endpoint: Optional[str] = None,
        graph_root_url: Optional[str] = None,
        graph_root_endpoint: Optional[str] = None,
        output_path: Optional[str] = None,
    ):
        # core configuration values
        self.tenant_id = tenant_id
        self.client_id = client_id
        self.client_secret = client_secret
        self.scope = scope

        self.graph_drive_id = graph_drive_id
        self.graph_file_id = graph_file_id
        self.graph_worksheet_id = graph_worksheet_id

        self.idp_url = idp_url or IDP_URL
        self.token_endpoint = token_endpoint or TOKEN_ENDPOINT
        self.graph_root_url = graph_root_url or GRAPH_ROOT_URL
        self.graph_root_endpoint = graph_root_endpoint or GRAPH_ROOT_ENDPOINT

        # column defaults (can be header names or integer indices)
        self.key_col = key_col or "key"
        self.val_col = val_col or "val"

        # output (optional; determined globally when building multiple vars)
        self.output_path = output_path

        # username/password for the only supported flow
        self.username = username
        self.password = password

        # validate required
        missing = []
        required = {
            "username": self.username,
            "password": self.password,
            "tenant-id": self.tenant_id,
            "client-id": self.client_id,
            "client-secret": self.client_secret,
            "scope": self.scope,
            "graph-drive-id": self.graph_drive_id,
            "graph-file-id": self.graph_file_id,
            "graph-worksheet-id": self.graph_worksheet_id,
            "idp-url": self.idp_url,
            "token-endpoint": self.token_endpoint,
            "graph-root-url": self.graph_root_url,
            "graph-root-endpoint": self.graph_root_endpoint,
            "key-col": self.key_col,
            "val-col": self.val_col,
        }
        for k, v in required.items():
            if not v:
                missing.append(k)

        if missing:
            msg_lines = [f"Missing configuration values: {', '.join(missing)}"]
            msg_lines.append(
                "Ensure these keys are present in the YAML configuration or provided when constructing GraphLutPuller()."
            )
            msg = "\n".join(msg_lines)
            raise RuntimeError(msg)

        if not self.username or not self.password:
            raise RuntimeError(
                "Both 'username' and 'password' must be supplied for the supported credential flow."
            )

        # build token url and token body (include username/password)
        self.token_url = urljoin(self.idp_url, self.tenant_id + self.token_endpoint)
        self.token_body = {
            "grant_type": GRANT_TYPE,
            "scope": self.scope,
            "client_id": self.client_id,
            "client_secret": self.client_secret,
            "username": self.username,
            "password": self.password,
        }

        # build base worksheet url
        self.worksheet_url = urljoin(
            self.graph_root_url,
            self.graph_root_endpoint
            + f"/drives/{self.graph_drive_id}/items/{self.graph_file_id}/workbook/worksheets/{self.graph_worksheet_id}",
        )

        self.access_token: Optional[str] = None
        self.token_expires_at: Optional[float] = None

    def _get_access_token(self) -> str:
        if (
            self.access_token is None
            or self.token_expires_at is None
            or self.token_expires_at <= time.time()
        ):
            resp = requests.post(self.token_url, data=self.token_body, timeout=10)
            resp.raise_for_status()
            data = resp.json()
            self.access_token = data["access_token"]
            self.token_expires_at = time.time() + int(data.get("expires_in", 3600)) - 60
        return self.access_token

    def get_used_range_values(self) -> List[List[Any]]:
        """Return the usedRange values (2D array) for the configured worksheet.

        Uses the Microsoft Graph API: GET {worksheet_url}/usedRange?$select=values
        """
        url = self.worksheet_url + "/usedRange?$select=values"
        headers = {"Authorization": f"Bearer {self._get_access_token()}"}
        resp = requests.get(url, headers=headers, timeout=10)
        resp.raise_for_status()
        data = resp.json()
        # Graph returns {'values': [[...],[...]]}
        values = data.get("values")
        if values is None:
            raise RuntimeError("Graph response did not contain 'values' in usedRange")
        return values

    @staticmethod
    def build_lut_from_values(
        var_name: str,
        values: List[List[Any]],
        key_col: Union[str, int] = "key",
        val_col: Union[str, int] = "val",
    ) -> Dict[str, str]:
        """Given worksheet values, build and return a LUT mapping "{var}/{key}" -> val.

        key_col/val_col may be header names (str) or column indices (int).
        The first row of values is treated as the header when column names are used.
        """
        if not values or len(values) < 1:
            return {}

        header = values[0]

        # resolve column indices for key and val
        def resolve(col: Union[str, int]) -> int:
            if isinstance(col, int):
                return col
            try:
                return header.index(col)
            except ValueError as exc:
                raise ValueError(
                    f"Column '{col}' not found in header: {header}"
                ) from exc

        key_i = resolve(key_col)
        val_i = resolve(val_col)

        lut: Dict[str, str] = {}
        for row in values[1:]:
            # some rows may be shorter than header
            def get_cell_from_row(row_local: List[Any], i: int) -> Optional[str]:
                if i < len(row_local):
                    v = row_local[i]
                    return None if v is None else str(v)
                return None

            key_cell = get_cell_from_row(row, key_i)
            val_cell = get_cell_from_row(row, val_i)

            if key_cell is None or val_cell is None:
                # skip incomplete rows
                continue

            # strip whitespace and ignore entries without a value
            key_str = key_cell.strip()
            val_str = val_cell.strip()
            if val_str == "":
                # no value -> do not add to LUT
                continue
            if key_str == "":
                # empty key is invalid; skip
                continue

            lut_key = f"{var_name}/{key_str}"
            lut[lut_key] = val_str

        return lut

    def save_lut(self, lut: Dict[str, str], path: Optional[str] = None) -> str:
        """Save the lut to disk as a newline-separated key=value text file.

        The canonical on-disk format is always text lines like:

            var/key=val

        Allowed file extensions: .txt, .properties, .lut. If the provided path
        has another extension, we warn and force a .txt extension.
        """
        # always treat as text key=value format
        path = path or self.output_path or "./lut.txt"
        p = Path(path)

        allowed_exts = {".txt", ".properties", ".lut"}
        ext = p.suffix.lower()
        if ext == "":
            # no extension -> append .txt
            p = p.with_suffix(".txt")
        elif ext not in allowed_exts:
            warnings.warn(
                f"Provided output extension '{ext}' is not one of {sorted(allowed_exts)}; saving as .txt instead."
            )
            p = p.with_suffix(".txt")

        p.parent.mkdir(parents=True, exist_ok=True)
        with p.open("w", encoding="utf-8") as f:
            for k, v in lut.items():
                f.write(f"{k}={v}\n")
        os.chmod(p, 0o666)

        return str(p)

    def pull_lut(
        self,
        var_name: str,
        key_col: Optional[Union[str, int]] = None,
        val_col: Optional[Union[str, int]] = None,
    ) -> Dict[str, str]:
        # Use constructor defaults when specific columns are not provided
        key_col = key_col if key_col is not None else self.key_col
        val_col = val_col if val_col is not None else self.val_col

        values = self.get_used_range_values()
        return self.build_lut_from_values(
            var_name, values, key_col=key_col, val_col=val_col
        )


def main():
    import argparse

    p = argparse.ArgumentParser(
        description="Pull one or more LUT variables from Microsoft Graph using YAML configuration."
    )

    p.add_argument(
        "config",
        nargs="+",
        help="One or more YAML files describing LUT variable configurations.",
    )

    args = p.parse_args()

    configs = load_variable_configs(args.config)
    grouped = group_configs_by_output_path(configs)

    if not grouped:
        raise RuntimeError("No variables were loaded; nothing to do.")

    for output_path, variables in grouped.items():
        combined_lut: Dict[str, str] = {}
        last_puller: Optional[GraphLutPuller] = None

        for var_name, cfg in variables:
            puller = GraphLutPuller(
                tenant_id=cfg.tenant_id,
                client_id=cfg.client_id,
                client_secret=cfg.client_secret,
                scope=cfg.scope,
                username=cfg.username,
                password=cfg.password,
                graph_drive_id=cfg.graph_drive_id,
                graph_file_id=cfg.graph_file_id,
                graph_worksheet_id=cfg.graph_worksheet_id,
                idp_url=cfg.idp_url,
                token_endpoint=cfg.token_endpoint,
                graph_root_url=cfg.graph_root_url,
                graph_root_endpoint=cfg.graph_root_endpoint,
                output_path=output_path,
                key_col=cfg.key_col,
                val_col=cfg.val_col,
            )
            last_puller = puller

            lut_piece = puller.pull_lut(
                var_name,
                key_col=cfg.key_col,
                val_col=cfg.val_col,
            )
            combined_lut.update(lut_piece)

        if last_puller is None:
            continue

        out_path = last_puller.save_lut(combined_lut, output_path)
        print(f"Wrote LUT to: {out_path}")


if __name__ == "__main__":
    main()
