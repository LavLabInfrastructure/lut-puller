import tempfile
import textwrap
import unittest
import warnings
from pathlib import Path

from lut_puller import (
    VariableConfig,
    group_configs_by_output_path,
    load_variable_configs,
)


def _write_yaml(temp_dir: Path, name: str, contents: str) -> Path:
    path = temp_dir / name
    path.write_text(textwrap.dedent(contents), encoding="utf-8")
    return path


class ConfigLoaderTests(unittest.TestCase):
    def _base_yaml(self) -> str:
        return """
        widget:
          tenant-id: tenant
          client-id: client
          client-secret: secret
          scope: scope/.default
          username: user
          password: pass
          graph-drive-id: drive
          graph-file-id: file
          graph-worksheet-id: worksheet
        """

    def test_load_single_config(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            temp_dir = Path(tmp)
            config_path = _write_yaml(temp_dir, "config.yaml", self._base_yaml())

            configs = load_variable_configs([config_path])

            self.assertEqual(set(configs.keys()), {"widget"})
            cfg = configs["widget"]
            self.assertIsInstance(cfg, VariableConfig)
            self.assertEqual(cfg.tenant_id, "tenant")
            self.assertEqual(cfg.key_col, "key")
            self.assertIsNone(cfg.output_path)

    def test_duplicate_identical_definitions_warn(self) -> None:
        duplicate_yaml = self._base_yaml()
        with tempfile.TemporaryDirectory() as tmp:
            temp_dir = Path(tmp)
            first = _write_yaml(temp_dir, "first.yaml", duplicate_yaml)
            second = _write_yaml(temp_dir, "second.yaml", duplicate_yaml)

            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                configs = load_variable_configs([first, second])

            self.assertEqual(set(configs.keys()), {"widget"})
            self.assertTrue(
                any("identical settings" in str(w.message) for w in caught),
                "Expected a warning about identical duplicate variable definitions.",
            )

    def test_conflicting_duplicates_raise(self) -> None:
        base = self._base_yaml()
        conflict = base.replace("graph-file-id: file", "graph-file-id: other")
        with tempfile.TemporaryDirectory() as tmp:
            temp_dir = Path(tmp)
            first = _write_yaml(temp_dir, "first.yaml", base)
            second = _write_yaml(temp_dir, "conflict.yaml", conflict)

            with self.assertRaises(RuntimeError):
                load_variable_configs([first, second])

    def test_group_configs_by_output_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            temp_dir = Path(tmp)

            config_text = textwrap.dedent(
                """
                widget:
                  tenant-id: tenant
                  client-id: client
                  client-secret: secret
                  scope: scope/.default
                  username: user
                  password: pass
                  graph-drive-id: drive
                  graph-file-id: file
                  graph-worksheet-id: worksheet

                gadget:
                  tenant-id: tenant
                  client-id: client
                  client-secret: secret
                  scope: scope/.default
                  username: user
                  password: pass
                  graph-drive-id: drive
                  graph-file-id: file2
                  graph-worksheet-id: worksheet
                  output-path: ./lut.txt
                """
            )
            config_path = _write_yaml(temp_dir, "config.yaml", config_text)

            configs = load_variable_configs([config_path])
            grouped = group_configs_by_output_path(configs)
            self.assertIn("./lut.txt", grouped)
            self.assertEqual(
                {name for name, _ in grouped["./lut.txt"]}, {"widget", "gadget"}
            )

            conflicting = textwrap.dedent(
                """
                doodad:
                  tenant-id: tenant
                  client-id: client
                  client-secret: secret
                  scope: scope/.default
                  username: user
                  password: pass
                  graph-drive-id: drive
                  graph-file-id: file3
                  graph-worksheet-id: worksheet
                  output-path: ./other.txt
                """
            )
            conflict_path = _write_yaml(temp_dir, "conflict.yaml", conflicting)

            configs = load_variable_configs([config_path, conflict_path])
            grouped = group_configs_by_output_path(configs)
            self.assertIn("./other.txt", grouped)
            self.assertEqual({name for name, _ in grouped["./other.txt"]}, {"doodad"})
            self.assertIn("./lut.txt", grouped)
            self.assertEqual(
                {name for name, _ in grouped["./lut.txt"]}, {"widget", "gadget"}
            )

    def test_missing_required_field_raises(self) -> None:
        bad_yaml = """
        widget:
          client-id: present
          client-secret: secret
          scope: scope/.default
          username: user
          password: pass
          graph-drive-id: drive
          graph-file-id: file
          graph-worksheet-id: worksheet
        """
        with tempfile.TemporaryDirectory() as tmp:
            temp_dir = Path(tmp)
            config_path = _write_yaml(temp_dir, "bad.yaml", bad_yaml)

            with self.assertRaises(RuntimeError):
                load_variable_configs([config_path])


if __name__ == "__main__":
    unittest.main()
