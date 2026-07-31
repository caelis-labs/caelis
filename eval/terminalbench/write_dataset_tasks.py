"""Print the task IDs from the pinned Terminal-Bench 2.1 dataset."""

import asyncio

from harbor.registry.client.package import PackageDatasetClient


EXPECTED_DIGEST = "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
DATASET = f"terminal-bench/terminal-bench-2-1@{EXPECTED_DIGEST}"


async def main() -> None:
    metadata = await PackageDatasetClient().get_dataset_metadata(DATASET)
    if metadata.version != EXPECTED_DIGEST:
        raise RuntimeError(
            f"Terminal-Bench 2.1 digest changed: {metadata.version}; expected {EXPECTED_DIGEST}"
        )
    for task_id in sorted(task.get_name() for task in metadata.task_ids):
        print(task_id)


if __name__ == "__main__":
    asyncio.run(main())
