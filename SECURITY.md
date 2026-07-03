# Security Policy

## Supported Versions

Only the latest tagged release receives security fixes.

## Reporting a Vulnerability

Please **do not** open a public issue for security vulnerabilities.

Report privately via
[GitHub private vulnerability reporting](https://github.com/Datata1/mycelium/security/advisories/new)
or by email to jan-david.wiederstein@codesphere.com.

Include a description of the issue, steps to reproduce, and the affected
version (`myco --version`). You will receive an acknowledgement within a few
days; please allow time for a fix before any public disclosure.

## Scope notes

Mycelium is a local-first tool: the daemon listens on a unix socket and an
HTTP loopback port (`127.0.0.1:7777`), and never makes network calls during
indexing. Reports about remote exposure of the loopback listener,
path-traversal via query parameters, or malicious-repository parsing crashes
are all in scope.
