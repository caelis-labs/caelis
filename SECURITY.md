# Security policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through
[GitHub private vulnerability reporting](https://github.com/caelis-labs/caelis/security/advisories/new).
Do not disclose an unpatched vulnerability in a public issue, discussion, or
pull request.

Include the affected version or commit, operating system, installation method,
impact, and minimal reproduction steps. Use synthetic data and redact tokens,
credentials, private prompts, and workspace contents from logs and examples.

Security reports may concern approval or sandbox bypasses, workspace trust,
Host authentication, cross-session data access, credential handling, or the
installation and update path. Ordinary bugs and feature requests belong in
public issues.

## Versions and disclosure

Please check whether the issue also affects the latest release. Older releases
have no guaranteed security backports; report the exact version you tested even
if you cannot upgrade.

Maintainers investigate reports and coordinate remediation and disclosure in
the private advisory. Response and remediation times depend on impact and
maintainer availability; no fixed response deadline is promised.

Only test systems and data you are authorized to assess. Avoid accessing other
users' data or disrupting services while reproducing a vulnerability.
