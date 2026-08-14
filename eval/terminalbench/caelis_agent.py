"""Harbor installed-agent adapter for the local Caelis headless binary."""

from pathlib import Path, PurePosixPath
import shlex
from typing import override

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.trial.paths import EnvironmentPaths

from eval.terminalbench.trace_usage import collect_trace_usage


class CaelisAgent(BaseInstalledAgent):
    """Run a locally built Caelis binary inside one Harbor task environment."""

    _REMOTE_BINARY = PurePosixPath("/usr/local/bin/caelis")
    _REMOTE_STORE = PurePosixPath("/opt/caelis-store")
    _REMOTE_CA_BUNDLE = _REMOTE_STORE / "cacert.pem"
    _OUTPUT_FILENAME = "caelis.jsonl"
    _STDERR_FILENAME = "caelis.stderr"

    def __init__(
        self,
        *args,
        binary_path: str,
        config_path: str,
        credential_path: str,
        credential_name: str,
        ca_bundle_path: str,
        reasoning_effort: str = "high",
        **kwargs,
    ):
        self._binary_path = Path(binary_path).expanduser().resolve()
        self._config_path = Path(config_path).expanduser().resolve()
        self._credential_path = Path(credential_path).expanduser().resolve()
        self._ca_bundle_path = Path(ca_bundle_path).expanduser().resolve()
        self._credential_name = Path(credential_name).name
        self._reasoning_effort = reasoning_effort.strip() or "high"
        for label, path in (
            ("binary", self._binary_path),
            ("config", self._config_path),
            ("credential", self._credential_path),
            ("CA bundle", self._ca_bundle_path),
        ):
            if not path.is_file():
                raise ValueError(f"Caelis {label} path is not a file: {path}")
        if self._credential_name != credential_name or not self._credential_name.endswith(".json"):
            raise ValueError("credential_name must be one JSON file name")
        super().__init__(*args, **kwargs)

    @staticmethod
    @override
    def name() -> str:
        return "caelis-headless"

    @override
    def get_version_command(self) -> str | None:
        return f"{self._REMOTE_BINARY} version"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        credential_dir = self._REMOTE_STORE / "providers" / "credentials"
        remote_config = self._REMOTE_STORE / "config.json"
        remote_credential = credential_dir / self._credential_name
        await self.exec_as_root(
            environment,
            command=(
                f"mkdir -p {shlex.quote(str(credential_dir))} && "
                f"chmod 700 {shlex.quote(str(self._REMOTE_STORE))} "
                f"{shlex.quote(str(credential_dir))}"
            ),
        )
        await environment.upload_file(self._binary_path, str(self._REMOTE_BINARY))
        await environment.upload_file(self._config_path, str(remote_config))
        await environment.upload_file(self._credential_path, str(remote_credential))
        await environment.upload_file(self._ca_bundle_path, str(self._REMOTE_CA_BUNDLE))

        owner = environment.default_user
        owner_command = ""
        if owner is not None:
            quoted_owner = shlex.quote(str(owner))
            owner_command = (
                f"chown -R {quoted_owner} {shlex.quote(str(self._REMOTE_STORE))} && "
                f"chown {quoted_owner} {shlex.quote(str(self._REMOTE_BINARY))} && "
            )
        await self.exec_as_root(
            environment,
            command=(
                owner_command
                + f"chmod 755 {shlex.quote(str(self._REMOTE_BINARY))} && "
                + f"chmod 600 {shlex.quote(str(remote_config))} "
                + f"{shlex.quote(str(remote_credential))} && "
                + f"chmod 644 {shlex.quote(str(self._REMOTE_CA_BUNDLE))}"
            ),
        )

    @with_prompt_template
    @override
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        if not self.model_name:
            raise ValueError("Caelis ModelProfile ID is required")
        output_path = EnvironmentPaths.agent_dir / self._OUTPUT_FILENAME
        stderr_path = EnvironmentPaths.agent_dir / self._STDERR_FILENAME
        command = self._command(instruction, output_path, stderr_path)
        await self.exec_as_agent(environment, command=command)

    def _command(
        self,
        instruction: str,
        output_path: PurePosixPath,
        stderr_path: PurePosixPath,
    ) -> str:
        command = " ".join(
            [
                f"SSL_CERT_FILE={shlex.quote(str(self._REMOTE_CA_BUNDLE))}",
                shlex.quote(str(self._REMOTE_BINARY)),
                "-p",
                shlex.quote(instruction),
                "-format jsonl",
                "-store-dir",
                shlex.quote(str(self._REMOTE_STORE)),
                "-workspace-key terminal-bench",
                '-workspace-cwd "$PWD"',
                "-model-profile",
                shlex.quote(self.model_name),
                "-reasoning-effort",
                shlex.quote(self._reasoning_effort),
                "-policy-profile workspace-write",
                "--dangerously-skip-permissions",
                f"2> {shlex.quote(str(stderr_path))} </dev/null | "
                f"tee {shlex.quote(str(output_path))}",
            ]
        )
        return command

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        output_path = self.logs_dir / self._OUTPUT_FILENAME
        usage = collect_trace_usage(output_path)
        context.n_input_tokens = usage["n_input_tokens"]
        context.n_cache_tokens = usage["n_cache_tokens"]
        context.n_output_tokens = usage["n_output_tokens"]
        metadata = dict(context.metadata or {})
        metadata["execution_mode"] = "dangerously-skip-permissions"
        metadata["model_profile_id"] = self.model_name
        metadata["reasoning_effort"] = self._reasoning_effort
        metadata["output_contract"] = "caelis.headless/v1"
        metadata["output_path"] = str(EnvironmentPaths.agent_dir / self._OUTPUT_FILENAME)
        metadata["reasoning_tokens"] = usage["n_reasoning_tokens"]
        metadata["total_tokens"] = usage["n_total_tokens"]
        metadata["context_window_tokens"] = usage["context_window_tokens"]
        metadata["cost_micros"] = usage["cost_micros"]
        metadata["usage_updates"] = usage["usage_updates"]
        metadata["usage_coverage"] = usage["usage_coverage"]
        context.metadata = metadata
