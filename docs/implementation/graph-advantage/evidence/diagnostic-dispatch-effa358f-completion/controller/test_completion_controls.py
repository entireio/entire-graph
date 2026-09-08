import errno
import importlib.util
import json
from pathlib import Path
import re
import socket
import tempfile
import types
import unittest


HERE = Path(__file__).resolve().parent
PACKAGE = HERE.parent


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


collector = load_module("completion_controls_collector", PACKAGE / "collector/run_remote.py")
observer = load_module("completion_controls_observer", PACKAGE / "collector/observe_remote.py")
controller = load_module("completion_controls_controller", HERE / "controller.py")


class CompletionRuntimeBoundaryTests(unittest.TestCase):
    def _fixture(self, root, overrides=None):
        root = Path(root)
        cgroup_path = "/system.slice/" + collector.EXPECTED_SYSTEMD_UNIT
        cgroup = root / "cgroup" / cgroup_path.lstrip("/")
        cgroup.mkdir(parents=True)
        (cgroup / "memory.max").write_text(str(collector.EXPECTED_MEMORY_MAX_BYTES) + "\n")
        (cgroup / "pids.max").write_text(str(collector.EXPECTED_TASKS_MAX) + "\n")
        cgroup_file = root / "self.cgroup"
        cgroup_file.write_text(f"0::{cgroup_path}\n")
        self_netns = root / "self.netns"
        init_netns = root / "init.netns"
        self_netns.write_text("private\n")
        init_netns.write_text("host\n")
        properties = {
            "LoadState": "loaded",
            "MemoryMax": str(collector.EXPECTED_MEMORY_MAX_BYTES),
            "TasksMax": str(collector.EXPECTED_TASKS_MAX),
            "PrivateNetwork": "yes",
            "RestrictAddressFamilies": "AF_UNIX",
            "KillMode": "control-group",
            "RuntimeMaxUSec": "infinity",
            "OOMPolicy": "kill",
            "Restart": "no",
            "RemainAfterExit": "yes",
            "ControlGroup": cgroup_path,
        }
        properties.update(overrides or {})

        def run(command, capture_output, text):
            self.assertEqual(command[0], collector.SYSTEMCTL_BINARY)
            self.assertTrue(capture_output)
            self.assertTrue(text)
            output = "".join(f"{key}={value}\n" for key, value in properties.items())
            return types.SimpleNamespace(returncode=0, stdout=output)

        def deny_network_family(family, kind):
            self.assertIn(family, (socket.AF_INET, socket.AF_INET6))
            self.assertEqual(kind, socket.SOCK_STREAM)
            raise OSError(errno.EAFNOSUPPORT, "address family blocked")

        return {
            "run": run,
            "cgroup_file": cgroup_file,
            "cgroup_root": root / "cgroup",
            "self_netns": self_netns,
            "init_netns": init_netns,
            "socket_factory": deny_network_family,
        }, properties, cgroup_path

    def test_accepts_exact_effective_systemd_cgroup_and_private_network_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            arguments, properties, cgroup_path = self._fixture(root)
            evidence = root / "runtime-boundary.json"

            record = collector.verify_runtime_boundary(
                evidence, collector.EXPECTED_SYSTEMD_UNIT, **arguments
            )

            self.assertEqual(record["status"], "passed")
            self.assertFalse(record["product_started"])
            self.assertEqual(record["properties"], properties)
            self.assertEqual(record["cgroup_path"], cgroup_path)
            self.assertEqual(record["memory_max"], "15032385536")
            self.assertEqual(record["pids_max"], "512")
            self.assertTrue(record["network_namespace_isolated"])
            self.assertEqual(record["network_probes"], {
                "AF_INET": {"denied": True, "errno": errno.EAFNOSUPPORT},
                "AF_INET6": {"denied": True, "errno": errno.EAFNOSUPPORT},
            })
            self.assertEqual(json.loads(evidence.read_text()), record)

    def test_refuses_each_changed_systemd_resource_or_network_property(self):
        cases = {
            "MemoryMax": "15032385535",
            "TasksMax": "511",
            "RuntimeMaxUSec": "240000000",
            "PrivateNetwork": "no",
            "RestrictAddressFamilies": "AF_UNIX AF_INET",
        }
        for field, changed_value in cases.items():
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                arguments, _, _ = self._fixture(root, {field: changed_value})
                evidence = root / "runtime-boundary.json"

                with self.assertRaisesRegex(RuntimeError, re.escape(field)):
                    collector.verify_runtime_boundary(
                        evidence, collector.EXPECTED_SYSTEMD_UNIT, **arguments
                    )

                record = json.loads(evidence.read_text())
                self.assertEqual(record["status"], "refused")
                self.assertFalse(record["product_started"])

    def test_product_process_disables_go_timeout_and_waits_without_a_deadline(self):
        calls = []

        class Process:
            def wait(self, *args, **kwargs):
                calls.append((args, kwargs))
                return 0

        def process_factory(command, **kwargs):
            time_path = Path(command[command.index("-o") + 1])
            time_path.write_text("Maximum resident set size (kbytes): 1024\n")
            calls.append((command, kwargs))
            return Process()

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            result = collector.run_process(
                root, root / "p1-evaluator", {"GOMAXPROCS": "4"},
                process_factory=process_factory,
            )
            command, process_options = calls[0]
            wait_args, wait_options = calls[1]
            process_record = json.loads((root / "process.json").read_text())

        self.assertIn("-test.timeout=0", command)
        self.assertFalse(any(argument.startswith("-test.timeout=") and argument != "-test.timeout=0"
                             for argument in command))
        self.assertEqual(wait_args, ())
        self.assertEqual(wait_options, {})
        self.assertNotIn("timeout", process_options)
        self.assertTrue(process_options["start_new_session"])
        self.assertEqual(result, {"exit_code": 0, "peak_rss_bytes": 1024 * 1024})
        self.assertFalse(process_record["timed_out"])
        self.assertIsNone(process_record["timeout_seconds"])
        self.assertIsNone(process_record["product_deadline_seconds"])


class CompletionObserverTests(unittest.TestCase):
    def test_classifies_running_terminal_and_unknown_without_guessing(self):
        cases = (
            ({"LoadState": "loaded", "ActiveState": "active", "SubState": "running"}, "running"),
            ({"LoadState": "loaded", "ActiveState": "active", "SubState": "exited"}, "terminal"),
            ({"LoadState": "loaded", "ActiveState": "failed", "SubState": "failed",
              "ExecMainExitTimestampMonotonic": "123"}, "terminal"),
            ({"LoadState": "unknown"}, "unknown"),
            ({"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead",
              "ExecMainExitTimestampMonotonic": "0"}, "unknown"),
        )
        for properties, expected in cases:
            with self.subTest(expected=expected, properties=properties):
                self.assertEqual(observer.classify(properties), expected)

    def _properties(self, lifecycle):
        values = {name: "" for name in observer.PROPERTIES}
        values.update({
            "LoadState": "loaded",
            "ActiveState": "active",
            "SubState": "running",
            "Result": "success",
            "ExecMainCode": "exited",
            "ExecMainStatus": "0",
            "ExecMainStartTimestampMonotonic": "10",
            "ExecMainExitTimestampMonotonic": "0",
            "ControlGroup": "/system.slice/completion.service",
        })
        if lifecycle == "terminal":
            values.update({"SubState": "exited", "ExecMainExitTimestampMonotonic": "20"})
        return values

    def test_observe_preserves_running_terminal_and_unknown_lifecycle(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "claim-worker-1.json").write_text(json.dumps({"started": True}) + "\n")

            for expected in ("running", "terminal", "unknown"):
                with self.subTest(expected=expected):
                    if expected == "unknown":
                        run = lambda *args, **kwargs: types.SimpleNamespace(returncode=1, stdout="")
                    else:
                        properties = self._properties(expected)
                        output = "".join(f"{key}={properties[key]}\n" for key in observer.PROPERTIES)
                        run = lambda *args, output=output, **kwargs: types.SimpleNamespace(
                            returncode=0, stdout=output
                        )
                    record = observer.observe(
                        root, collector.EXPECTED_SYSTEMD_UNIT, run=run,
                        cgroup_root=root / "absent-cgroup-root",
                    )
                    self.assertEqual(record["lifecycle"], expected)
                    self.assertTrue(record["product_started"])
                    self.assertEqual(record["terminal_success"], expected == "terminal")


class CompletionDetachedStartTests(unittest.TestCase):
    def test_manifest_and_start_script_have_no_product_deadline(self):
        document = controller.read_manifest(require_unclaimed=False)
        self.assertIsNone(document.get("product_deadline_seconds"))
        self.assertIsNone(document.get("remote_control_timeout_seconds"))
        self.assertEqual(document.get("product_timeout_mode"), "none-user-authorized-completion")

        script = controller.remote_start_script(
            document,
            "https://example.invalid/control",
            "https://example.invalid/result",
        )

        for fragment in (
            "systemd-run", "--no-block", "MemoryMax=15032385536", "TasksMax=512",
            "PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX",
            "KillMode=control-group", "RuntimeMaxSec=infinity",
        ):
            with self.subTest(fragment=fragment):
                self.assertIn(fragment, script)
        self.assertNotIn("--scope", script)
        self.assertIsNone(re.search(r"(?:^|[;&|\s])(?:/usr/bin/)?timeout(?:\s|$)", script))
        self.assertIsNone(re.search(r"(?<![A-Za-z0-9])(?:120|130|240|250|360)(?![A-Za-z0-9])", script))


if __name__ == "__main__":
    unittest.main()
