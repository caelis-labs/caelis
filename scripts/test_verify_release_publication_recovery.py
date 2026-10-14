import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from scripts import verify_release_publication_recovery as recovery


class PublicationRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.receipt = self.root / "recovery" / "receipt"
        self.checkpoint = self.root / "recovery" / "checkpoint"
        self.dist = self.root / "dist"
        for directory in (self.receipt, self.checkpoint, self.dist):
            directory.mkdir(parents=True)
        binaries = {"amd64": b"signed-amd64", "arm64": b"signed-arm64"}
        for platform, arch in (("darwin", "amd64"), ("darwin", "arm64"),
                               ("linux", "amd64"), ("linux", "arm64"),
                               ("windows", "amd64"), ("windows", "arm64")):
            archive = self.dist / f"caelis_0.71.1_{platform}_{arch}.tar.gz"
            with tarfile.open(archive, "w:gz") as package:
                payload = binaries[arch] if platform == "darwin" else b"other"
                member = tarfile.TarInfo("./caelis")
                member.size = len(payload)
                package.addfile(member, io.BytesIO(payload))
        import hashlib
        self.hash = lambda data: hashlib.sha256(data).hexdigest()
        self.dist.joinpath("checksums.txt").write_text("".join(
            f"{self.hash(path.read_bytes())}  {path.name}\n" for path in sorted(self.dist.glob("*.tar.gz"))))
        submission = self.checkpoint / "submission.zip"
        with zipfile.ZipFile(submission, "w") as archive:
            for arch, payload in binaries.items():
                archive.writestr(f"notary/{arch}/caelis", payload)
        sha = self.hash(submission.read_bytes())
        self.write_json("submission-manifest.json", {
            "tag": recovery.TAG, "tag_sha": recovery.TAG_SHA,
            "source_run": str(recovery.BUILD_RUN), "submission_zip_sha256": sha,
        })
        self.write_json("submission.json", {"id": recovery.SUBMISSION_ID})
        self.write_json("result.json", {"id": recovery.SUBMISSION_ID, "status": "Accepted"})
        self.write_json("log.json", {"jobId": recovery.SUBMISSION_ID,
                                     "status": "Accepted", "sha256": sha})
        self.responses = {
            f"actions/runs/{recovery.SIGNING_RUN}": {
                "event": "workflow_dispatch", "head_sha": recovery.SIGNING_HEAD,
                "status": "completed", "conclusion": "success"},
            f"actions/runs/{recovery.SIGNING_RUN}/jobs?per_page=100": {"jobs": [
                {"name": name, "conclusion": status} for name, status in (
                    ("verify-recovery", "success"), ("notarize-darwin", "success"),
                    ("release", "skipped"), ("publish-r2", "skipped"))]},
            f"actions/runs/{recovery.SIGNING_RUN}/artifacts?per_page=100": {
                "total_count": 3, "artifacts": [
                    {"name": name, "size_in_bytes": size, "digest": sha, "expired": False}
                    for name, (size, sha) in recovery.ARTIFACTS.items()]},
            f"releases/tags/{recovery.TAG}": {
                "tag_name": recovery.TAG, "draft": False, "prerelease": False, "assets": []},
            "releases/latest": {"tag_name": recovery.TAG},
            "actions/workflows/release.yml/runs?event=workflow_dispatch&per_page=100": {
                "total_count": 2, "workflow_runs": [
                    {"id": recovery.SIGNING_RUN}, {"id": 123}]},
        }

    def write_json(self, name, content):
        self.receipt.joinpath(name).write_text(json.dumps(content))

    def check(self, expected=None):
        env = dict(os.environ, GITHUB_EVENT_NAME="workflow_dispatch",
                   GITHUB_REF="refs/heads/main", GITHUB_REPOSITORY=recovery.REPO,
                   GITHUB_RUN_ATTEMPT="1", GITHUB_RUN_ID="123", GH_TOKEN="fixture",
                   RELEASE_TAG=recovery.TAG, RELEASE_TAG_SHA=recovery.TAG_SHA,
                   RELEASE_SOURCE_RUN=str(recovery.BUILD_RUN))
        original_run = recovery.subprocess.run

        def run(args, **kwargs):
            if args[0] == "git":
                return None
            return original_run(args, **kwargs)

        with patch.dict(os.environ, env), patch.object(recovery, "api", side_effect=self.responses.__getitem__), \
             patch.object(recovery, "command", return_value=recovery.TAG_SHA), \
             patch.object(recovery.subprocess, "run", side_effect=run):
            if expected:
                with self.assertRaisesRegex(RuntimeError, expected):
                    recovery.verify(self.root / "recovery", self.dist)
            else:
                recovery.verify(self.root / "recovery", self.dist)

    def test_accepted_submission_and_release_bytes(self):
        self.check()

    def test_rejected_apple_receipt(self):
        self.write_json("result.json", {"id": recovery.SUBMISSION_ID, "status": "Invalid"})
        self.check("Apple Accepted receipt")

    def test_different_darwin_release_binary(self):
        archive = self.dist / "caelis_0.71.1_darwin_arm64.tar.gz"
        with tarfile.open(archive, "w:gz") as package:
            payload = b"different signed bytes"
            member = tarfile.TarInfo("./caelis")
            member.size = len(payload)
            package.addfile(member, io.BytesIO(payload))
        self.dist.joinpath("checksums.txt").write_text("".join(
            f"{self.hash(path.read_bytes())}  {path.name}\n" for path in sorted(self.dist.glob("*.tar.gz"))))
        self.check("arm64 release bytes differ")

    def test_existing_asset_or_extra_dispatch_refused(self):
        self.responses[f"releases/tags/{recovery.TAG}"]["assets"] = [{"name": "checksums.txt"}]
        self.check("release already has assets")
        self.responses[f"releases/tags/{recovery.TAG}"]["assets"] = []
        self.responses["actions/workflows/release.yml/runs?event=workflow_dispatch&per_page=100"]["total_count"] = 3
        self.check("unexpected recovery dispatch")


if __name__ == "__main__":
    unittest.main()
