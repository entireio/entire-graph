import hashlib
import importlib.util
import json
from pathlib import Path
import unittest
from validate_precleanup_attestation import verify as verify_attestation


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[3]
SUMMARY = HERE / "platform-observations.json"

spec = importlib.util.spec_from_file_location("platform_observations", HERE / "platform_observations.py")
platform_observations = importlib.util.module_from_spec(spec)
spec.loader.exec_module(platform_observations)


class PlatformObservationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.record = json.loads(SUMMARY.read_text())

    def test_summary_is_exact_reprojection_of_checkpointed_wrappers(self) -> None:
        self.assertEqual(verify_attestation(REPO,HERE/"precleanup-parity-attestation.json"),[])
        self.assertEqual(self.record["observation_count"], 86)
        self.assertEqual(
            sum(item["source_bytes"] for item in self.record["observations"]),
            self.record["source_bytes"],
        )

    def test_normalizer_projects_synthetic_transport_content(self) -> None:
        value={"value":[{"code":"ProvisioningState/succeeded","displayStatus":"Provisioning succeeded","level":"Info","time":"2026-01-01","message":"Upload succeeded"}]}
        kind,facts=platform_observations.normalize_payload("synthetic/transport.json",value)
        self.assertEqual(kind,"remote-command-status")
        self.assertEqual(facts["statuses"][0]["message_facts"]["status_word_counts"]["succeeded"],1)
        value["value"][0]["message"]="Upload failed"
        self.assertNotEqual(platform_observations.normalize_payload("synthetic/transport.json",value)[1],facts)

    def test_expected_operational_families_are_preserved(self) -> None:
        kinds = {item["kind"] for item in self.record["observations"]}
        self.assertEqual(
            kinds,
            {
                "redacted-terminal-status",
                "remote-command-status",
                "transport-configuration",
                "transport-output",
                "vm-cleanup-status",
                "vm-paused-state",
                "vm-power-state",
                "vm-terminal-state",
            },
        )
        terminals = [item for item in self.record["observations"] if item["kind"] == "vm-terminal-state"]
        self.assertEqual(len(terminals), 23)
        self.assertTrue(all(item["facts"]["resource_class"] for item in terminals))
        self.assertTrue(all(item["facts"]["instance_statuses"] for item in terminals))

    def test_public_record_excludes_provider_and_local_identifiers(self) -> None:
        data = SUMMARY.read_bytes()
        for forbidden in (
            b"/subscriptions/",
            b".blob.core.windows.net",
            b"/Users/",
            b"/home/",
            b"ssh-rsa",
            b'"resourceGroup"',
            b'"raw_response_redacted"',
            b'"adminUsername"',
        ):
            self.assertNotIn(forbidden, data)

    def test_removed_wrappers_are_bound_to_this_summary(self) -> None:
        removal = json.loads((HERE / "removal-map.json").read_text())
        candidates = {item["path"]: item for item in removal["candidates"]}
        retained_sha = hashlib.sha256(SUMMARY.read_bytes()).hexdigest()
        for item in self.record["observations"]:
            source_path = item["source_path"]
            self.assertFalse((REPO / source_path).exists())
            candidate = candidates[source_path]
            self.assertEqual(candidate["source_sha256"], item["source_sha256"])
            self.assertEqual(candidate["replacement"]["retained_sha256"], retained_sha)

    def test_duplicate_terminal_wrappers_share_one_sanitized_state(self) -> None:
        path = HERE / "duplicate-vm-terminal-observations.json"
        record = json.loads(path.read_text())
        self.assertEqual(record["alias_count"], 15)
        self.assertEqual(
            record["normalized_state"],
            [
                {"role": "worker-2", "power_state": "VM deallocated"},
                {"role": "worker-3", "power_state": "VM deallocated"},
                {"role": "validation", "power_state": "VM deallocated"},
            ],
        )
        removal = json.loads((HERE / "removal-map.json").read_text())
        candidates = {item["path"]: item for item in removal["candidates"]}
        retained_sha = hashlib.sha256(path.read_bytes()).hexdigest()
        for alias in record["aliases"]:
            self.assertFalse((REPO / alias["source_path"]).exists())
            candidate = candidates[alias["source_path"]]
            self.assertEqual(candidate["source_sha256"], alias["source_sha256"])
            self.assertEqual(candidate["replacement"]["retained_sha256"], retained_sha)


if __name__ == "__main__":
    unittest.main()
