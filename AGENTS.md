# AGENTS.md



## Instruction priority

1. Safety, security, and legal constraints.
2. Explicit user request for the current task.
3. Repository conventions and existing project structure.
4. This file.
5. General engineering preferences.

Ask a clarifying question only when ambiguity actually prevents a correct implementation; otherwise pick the smallest reasonable implementation consistent with the request and repo conventions.

## Scope discipline

Implement only what's necessary for the requested outcome. Unless explicitly asked, do not add:

- unrelated refactors, renames, or reformatting of nearby/unrelated code
- new abstractions, modules, or helper frameworks for a single use site or hypothetical reuse
- new dependencies (verify existing tooling/utilities can't do it first)
- new CI/CD stages, observability, dashboards, alerts, or policy frameworks
- a "golden path" / generalized platform solution when one service, chart, resource, or pipeline was asked for
- documentation unrelated to the change

A little duplication beats an unnecessary abstraction. 

