#!/usr/bin/env python3
"""Permit one v0.71.1 publication from the already accepted signing run.

The workflow downloads these artifacts from that run before calling this guard.
No code signing or Apple submission happens during this publication resume.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import zipfile


TAG = "v0.71.1"
TAG_SHA = "5e232e817cdd62186a19dd1d3a43aa5476326a74"
SIGNING_RUN = 38505541188
SIGNING_HEAD = "40baada2990c9315d15bacab45990c4876a73b32"
BUILD_RUN = 38420720149
SUBMISSION_ID = "df878ad8-1e0b-4c99-b562-90b2bf74eb9e"
REPO = "caelis-labs/caelis"
ARTIFACTS = {
    "macos-notarization-receipt": (1296, "sha256:7a95c6a1e20b3d8691b9b0dbe12305b145e6baea07fa820497e7f86929d67ddc"),
    "macos-signing-checkpoint": (62288951, "sha256:3fd76aa4eaaa3676402fe8a9887601bf30b178216af80917f448f0ec23062369"),
    "verified-release-archives": (184684686, "sha256:60acf0cedd0ecca78b61ccc681bd6c2d21a004dd64637d272c90bb5240590dc7"),
}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def api(path):
    return json.loads(command("gh", "api", f"repos/{REPO}/{path}"))


def one_file(root, name):
    matches = list(root.rglob(name))
    require(len(matches) == 1 and matches[0].is_file(), f"expected exactly one {name}")
    return matches[0]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def verify(recovery, dist):
    env = os.environ
    require(
        env.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
        and env.get("GITHUB_REF") == "refs/heads/main"
        and env.get("GITHUB_REPOSITORY") == REPO
        and env.get("GITHUB_RUN_ATTEMPT") == "1"
        and env.get("RELEASE_TAG") == TAG
        and env.get("RELEASE_TAG_SHA") == TAG_SHA
        and env.get("RELEASE_SOURCE_RUN") == str(BUILD_RUN)
        and env.get("GH_TOKEN")
        and env.get("GITHUB_RUN_ID", "").isdigit(),
        "publication resume requires its first dispatch attempt on Caelis main",
    )
    require(command("git", "rev-parse", f"refs/tags/{TAG}^{{commit}}") == TAG_SHA,
            "v0.71.1 tag commit changed")
    subprocess.run(["git", "merge-base", "--is-ancestor", TAG_SHA, "HEAD"], check=True)
    subprocess.run(["git", "merge-base", "--is-ancestor", SIGNING_HEAD, "HEAD"], check=True)
    subprocess.run(["git", "diff", "--quiet", TAG_SHA, "HEAD", "--", "npm", ".goreleaser.yaml",
                    "scripts/verify_release_archives.sh", "scripts/publish_release_assets.sh"], check=True)
    require(digest(Path("scripts/publish_latest_to_r2.sh").read_bytes()) ==
            "69121275b1ddc5cce677adc9a2b9193c4c214abbde60da32c0d03e35d51b372f",
            "reviewed R2 publication script changed")

    run = api(f"actions/runs/{SIGNING_RUN}")
    require(run.get("event") == "workflow_dispatch" and run.get("head_sha") == SIGNING_HEAD
            and run.get("status") == "completed" and run.get("conclusion") == "success",
            "accepted signing run identity or status changed")
    jobs = {job["name"]: job["conclusion"] for job in api(f"actions/runs/{SIGNING_RUN}/jobs?per_page=100")["jobs"]}
    require(jobs.get("verify-recovery") == "success" and jobs.get("notarize-darwin") == "success"
            and jobs.get("release") == "skipped" and jobs.get("publish-r2") == "skipped",
            "signing run did not stop at the reviewed publication boundary")
    artifacts = api(f"actions/runs/{SIGNING_RUN}/artifacts?per_page=100")
    found = {a["name"]: a for a in artifacts["artifacts"]}
    require(artifacts["total_count"] == len(ARTIFACTS) and set(found) == set(ARTIFACTS),
            "signing artifact set changed")
    for name, (size, sha) in ARTIFACTS.items():
        require(found[name].get("size_in_bytes") == size and found[name].get("digest") == sha
                and found[name].get("expired") is False, f"{name} is missing, expired or changed")

    release = api(f"releases/tags/{TAG}")
    require(release.get("tag_name") == TAG and release.get("draft") is False
            and release.get("prerelease") is False and release.get("assets") == [],
            "release already has assets; inspect before publication")
    require(api("releases/latest").get("tag_name") == TAG,
            "v0.71.1 is no longer latest; refusing R2 publication")

    # A prior publication dispatch may have partially published npm or GitHub
    # assets. Such a retry must use its own receipt and be reviewed separately.
    current = int(env["GITHUB_RUN_ID"])
    recoveries = api("actions/workflows/release.yml/runs?event=workflow_dispatch&per_page=100")
    ids = {run["id"] for run in recoveries["workflow_runs"]}
    require(recoveries["total_count"] == 2 and ids == {SIGNING_RUN, current},
            "unexpected recovery dispatch; refusing publication")

    receipt = recovery / "receipt"
    checkpoint = recovery / "checkpoint"
    submission = one_file(checkpoint, "submission.zip")
    manifest = json.loads(one_file(receipt, "submission-manifest.json").read_text())
    submitted = json.loads(one_file(receipt, "submission.json").read_text())
    result = json.loads(one_file(receipt, "result.json").read_text())
    log = json.loads(one_file(receipt, "log.json").read_text())
    submission_sha = digest(submission.read_bytes())
    require(manifest == {"tag": TAG, "tag_sha": TAG_SHA, "source_run": str(BUILD_RUN),
                         "submission_zip_sha256": submission_sha},
            "signed submission manifest does not match the original tag and ZIP")
    require(submitted.get("id") == SUBMISSION_ID and result.get("id") == SUBMISSION_ID
            and result.get("status") == "Accepted" and log.get("jobId") == SUBMISSION_ID
            and log.get("status") == "Accepted" and log.get("sha256") == submission_sha,
            "Apple Accepted receipt does not match the exact signed ZIP")

    subprocess.run(["bash", "scripts/verify_release_archives.sh", TAG[1:], str(dist)], check=True)
    with zipfile.ZipFile(submission) as signed_zip:
        for arch in ("amd64", "arm64"):
            members = [name for name in signed_zip.namelist() if name.endswith(f"/{arch}/caelis")]
            require(len(members) == 1, f"signed submission missing {arch} binary")
            signed_sha = digest(signed_zip.read(members[0]))
            archive = dist / f"caelis_{TAG[1:]}_darwin_{arch}.tar.gz"
            with tarfile.open(archive, "r:gz") as package:
                members = [member for member in package if member.name in ("caelis", "./caelis") and member.isfile()]
                require(len(members) == 1, f"release archive missing {arch} binary")
                release_sha = digest(package.extractfile(members[0]).read())
            require(signed_sha == release_sha, f"{arch} release bytes differ from Apple Accepted submission")
    print(f"Verified {TAG} publication from Accepted Apple submission {SUBMISSION_ID} in run {SIGNING_RUN}.")


if __name__ == "__main__":
    try:
        require(len(sys.argv) == 3, "usage: verify_release_publication_recovery.py RECEIPT_DIR DIST_DIR")
        verify(Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve())
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError, RuntimeError,
            tarfile.TarError, zipfile.BadZipFile) as exc:
        raise SystemExit(f"publication recovery refused: {exc}") from None
