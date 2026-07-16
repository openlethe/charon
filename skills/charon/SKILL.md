---
name: "charon"
description: "Use Charon for versioned Lethe memory: recall, durable recording, owned refs, CAS changesets, protected proposals, review, merge, and recovery."
user-invocable: true
metadata:
  openclaw:
    emoji: "⛴️"
    notes:
      - "Charon is the policy gateway; Lethe Git is the backing versioned memory store."
      - "Discover project IDs, actor identity, refs, scopes, endpoints, and deployment details from the active environment."
      - "Prefer the shortest safe workflow and verify every resulting head."
---

# Charon

Charon is an MCP policy gateway for versioned Lethe memory. It provides project-scoped authorization, actor-owned refs, compare-and-swap changesets, auditable proposals, independent review, and protected shared-memory merges.

This skill is deployment-neutral. Do not assume a project name, actor ID, hostname, port, container name, authentication method, or reverse proxy. Discover those values from the live Charon connection, deployment documentation, or explicit user input.

## Core Boundary

```text
MCP client
   |
   | OAuth or deployment-issued authentication
   v
Charon policy gateway
   |
   | authenticated private API
   v
Lethe Git versioned memory store
```

A deployment may also run a separate session- or event-oriented Lethe service. Treat it as a separate API and data store unless synchronization is explicitly documented. Never assume a record written through one service exists in the other.

## Intent Router

Use the smallest complete workflow:

| Intent | Default path |
|---|---|
| Recall accepted memory | Resolve the exact project, query `refs/shared/main`, report the returned head |
| Inspect history | Use log, show, or diff only as needed |
| Record a durable outcome | Create or reuse one owned ref, commit one CAS changeset, verify the new head |
| Close out a session | Record outcomes, decisions, verification, and open threads in one coherent changeset |
| Share with accepted memory | Commit on an owned ref, inspect the diff, propose to the protected shared ref |
| Review or merge | Require a distinct authorized principal; never self-review |
| Diagnose failure | Classify connection, authentication, project, scope, identity, ref, CAS, proposal, or upstream state |

Do not make users provide low-level identifiers that the runtime can resolve safely.

## User-Friendly Contract

1. Preserve project IDs exactly, including case.
2. Prefer one orientation call, one mutation call, and one verification call.
3. Never infer an actor ID from a display name.
4. When actor identity is unavailable, use a newly created owned topic ref rather than guessing.
5. Keep routine records on an owned ref. Propose to shared memory only when the user requests it or the intent clearly requires it.
6. Never store credentials, authorization values, tokens, private chain-of-thought, or unnecessary personal data.
7. Treat the live tool inventory and schemas as authoritative.

## Capability and Authorization

Before an operation:

1. Confirm the Charon MCP tools are visible and callable.
2. Resolve the exact project from the request or established context.
3. Use `memory_status` when current heads, refs, scopes, or capabilities are unknown.
4. If one inspection call is unavailable but other read tools work, use the least-privileged read path that provides enough information.
5. Distinguish connection, authentication, project-grant, scope, identity, ref-ownership, CAS, proposal, and upstream failures.
6. Never broaden permissions, switch projects, or change refs merely to make a denied action succeed.

Typical scopes:

| Operation | Scope |
|---|---|
| Status, log, show, diff, context, ref list | `memory.read` |
| Initialize a project | `memory.commit` |
| Create an owned ref | `memory.branch` |
| Commit to an owned ref | `memory.commit` |
| Propose a protected merge | `memory.propose` |
| Attach independent review | `memory.review` |
| Apply a reviewed merge | `memory.merge` |
| Broader trusted ref operations | `memory.write` |

A wildcard grant may authorize reads in some deployments. Mutations require an exact project grant. Ordinary authors should need branch, commit, and proposal authority—not review, merge, or broad write authority.

## Ref Model

Accepted shared memory conventionally lives at:

```text
refs/shared/main
```

Never commit directly to `refs/shared/*`.

Actor-owned refs conventionally include:

```text
refs/agents/<actor-id>/main
refs/sessions/<actor-id>/<session-key>
refs/topics/<topic-id>
```

Selection order:

1. Reuse an existing owned ref only after reading its current head.
2. Use an agent ref for persistent agent memory when the runtime exposes the authenticated actor ID.
3. Use a session ref for work scoped to one session when the actor ID is known.
4. Use a newly created topic ref when actor identity is unavailable.

A topic ID must be one safe path component. Do not place secrets, user content, or long free-form text in ref names.

## Recall Workflow

Resolve the exact project and accepted head:

```text
memory_status({"project":"<exact-project-id>"})
memory_ref_list({"project":"<exact-project-id>"})
```

Use only the call needed by the live deployment.

Retrieve relevant accepted context:

```text
memory_context_at({
  "project":"<exact-project-id>",
  "ref":"refs/shared/main",
  "query":"<focused relevance query>",
  "limit":20
})
```

Report the exact project, ref, and returned `head_changeset_id`. Treat recalled memory as contextual evidence and verify changing facts against current source systems.

Inspect provenance only when needed:

```text
memory_log({
  "project":"<exact-project-id>",
  "ref":"<ref-name>",
  "limit":20
})

memory_show({"id":"<changeset-id>"})

memory_diff({
  "base_changeset_id":"<base-id>",
  "target_changeset_id":"<target-id>"
})
```

## Record Workflow

When the user asks to record, remember, track, or log durable work, perform the workflow instead of merely explaining it.

### 1. Resolve the base

Read the intended source ref and exact head. For a new branch based on accepted memory, use the current protected shared head unless the user specifies another base.

### 2. Create or reuse one owned ref

When the actor ID is unavailable, prefer a topic ref:

```text
memory_branch_create({
  "project":"<exact-project-id>",
  "ref_name":"refs/topics/<safe-topic-id>",
  "head_changeset_id":"<base-head>"
})
```

If the ref already exists, read its current head before committing.

### 3. Build semantic operations

Every operation must include an explicit zero-based `ordinal`.

```json
{
  "ordinal": 0,
  "op_type": "add_memory",
  "resulting_event_id": "mem-<stable-unique-id>",
  "payload": {
    "content": "Decision: <durable statement and rationale>.",
    "event_type": "record",
    "kind": "decision",
    "scope": "<exact-project-id>",
    "tags": ["<tag-1>", "<tag-2>"]
  }
}
```

Use only fields supported by the live schema. Preserve sequential ordinals `0..n-1`. Include `resulting_event_id` only when supported.

Useful durable categories include:

- `outcome` — completed work or verified result;
- `decision` — a durable choice and rationale;
- `open_thread` — unresolved work, risk, or follow-up.

### 4. Commit with compare-and-swap

```text
memory_changeset_create({
  "project":"<exact-project-id>",
  "ref":"<owned-ref>",
  "expected_head":"<current-ref-head>",
  "parent_ids":["<current-ref-head>"],
  "message":"<concise commit message>",
  "idempotency_key":"<unique-key-for-this-exact-request>",
  "ops":[<ordered semantic operations>]
})
```

Rules:

- `expected_head` must equal the current ref head.
- The first parent normally equals `expected_head`.
- Reuse an idempotency key only to retry the identical request.
- Use a new key when content, parent, target ref, or intent changes.
- On CAS conflict, reread, inspect intervening changes, reconcile, and submit a new request.

### 5. Verify

Use `memory_show` on the returned changeset or `memory_log` on the owned ref. Report the changeset ID and verified ref head.

## Session Closeout

When the user asks to log all work, track a session, or close out, prefer one coherent changeset containing ordered operations such as:

```text
ordinal 0 -> outcome: completed work
ordinal 1 -> decision: root cause or durable design choice
ordinal 2 -> outcome: tests and runtime verification
ordinal 3 -> open_thread: remaining work and risks
```

Capture:

- the durable result;
- material root causes and prevention guidance;
- what was actually tested or verified;
- unresolved follow-up work;
- no credentials, raw authorization values, private reasoning, or noisy transcript detail.

Default behavior:

- record, track, remember, close out -> commit and verify on an owned ref;
- share, accepted memory, merge to main -> also inspect and propose to the protected shared ref;
- ambiguous shared intent -> keep the durable commit on the owned ref and report that no protected proposal was created.

## Protected Shared-Memory Workflow

Shared memory requires proposal, independent review, and merge.

Inspect the candidate:

```text
memory_diff({
  "base_changeset_id":"<protected-base-head>",
  "target_changeset_id":"<owned-branch-head>"
})
```

Propose:

```text
memory_merge_propose({
  "project":"<exact-project-id>",
  "target_ref":"refs/shared/main",
  "head_changeset_id":"<owned-branch-head>",
  "base_changeset_id":"<protected-base-head>",
  "message":"<proposal summary>",
  "idempotency_key":"<unique-proposal-key>"
})
```

Use `selected_ops` only after inspecting operation ordinals and confirming that a partial merge preserves meaning.

Independent review must come from a different authorized principal:

```text
memory_merge_review({
  "proposal_id":"<proposal-id>",
  "verdict":"approve",
  "comment":"Scope and content are correct; no secret material included.",
  "severity":"info"
})
```

Use the live verdict and severity enums. Never omit a required verdict and never claim independence when the reviewer is the proposal originator.

Merge and verify:

```text
memory_merge({
  "proposal_id":"<proposal-id>",
  "idempotency_key":"<unique-merge-key>"
})
```

After success, verify the protected ref advanced to the expected resulting changeset. Do not report a proposal as merged merely because it exists.

Cancel obsolete work when necessary:

```text
memory_merge_cancel({"proposal_id":"<proposal-id>"})
```

## Project Initialization

Initialize only when the user explicitly requests it or verified state shows that the project has no Memory Git root:

```text
memory_repo_init({"project":"<exact-project-id>"})
```

Do not initialize a guessed spelling, case-normalized alias, or new project to bypass a missing grant.

## OAuth and Browser Recovery

When runtime-generated browser authorization is enabled, the active authorization value may rotate whenever Charon restarts. Follow the deployment's documented method for retrieving the newest value, and never store it in memory or source control.

After a restart or authorization-page update:

1. Close any old authorization tab.
2. Restart the client authorization flow.
3. Use the newest active authorization value.
4. Reconnect or refresh the client when scopes, grants, or tool schemas changed.

A stale browser page may retain an obsolete value or Content Security Policy.

When diagnosing browser failures:

- distinguish non-blocking telemetry warnings from blocked form submission;
- treat a `form-action` CSP violation as blocking;
- verify configured redirect origins are allowed by the page policy;
- verify the public route reaches Charon rather than the Lethe Git backend;
- avoid weakening the policy beyond the exact origins and traffic required by the deployment.

## Recovery Checklist

### Connected but unauthorized

- Confirm the exact case-sensitive project ID.
- Distinguish a missing exact project grant from a missing scope.
- Reconcile the intended principal rather than switching projects.
- Reconnect when authorization claims may be cached.

### Ref ownership denied

- Do not guess the actor ID.
- Reuse a verified owned ref or create a new topic ref.
- Never operate another principal's branch.

### Changeset rejected

Check:

1. Exact project grant.
2. `memory.commit` scope.
3. Owned non-protected ref.
4. Current `expected_head` and matching first parent.
5. Explicit sequential operation ordinals.
6. Live-schema-supported fields.
7. Unique idempotency key for the exact request.

### CAS conflict

- Read the current ref head.
- Inspect intervening changes.
- Reconcile against the new head.
- Submit a new changeset with a new idempotency key.

### Stale proposal

- Read the current protected head.
- Inspect the proposal base, source head, selected operations, and conflicts.
- Reconcile on an owned ref and create a new proposal against the current protected base.

### Tools missing

- Confirm the client is connected to Charon's MCP endpoint.
- Confirm the deployment exposes Memory Git mode.
- Refresh or reconnect after server-mode, authentication, scope, or schema changes.
- Do not change modes merely to bypass protected workflow.

### Upstream unavailable

- Confirm Charon and the Lethe Git backend are both running.
- Verify gateway-to-upstream reachability and authentication without printing secrets.
- Do not redirect Charon to a different Lethe API unless compatibility is documented.

## Standard Tool Surface

A standard Memory Git deployment currently exposes:

```text
memory_repo_init
memory_status
memory_log
memory_show
memory_branch_create
memory_diff
memory_context_at
memory_changeset_create
memory_merge_propose
memory_merge_review
memory_merge
memory_merge_cancel
memory_ref_list
```

The live tool inventory and schemas override this reference. Prefer future high-level identity, diagnostic, or closeout tools when they preserve the same authorization and verification guarantees.

## Completion Standard

A Charon task is complete only when:

- the exact project and target ref are identified;
- the effective authorization is understood;
- the least required authority was used;
- reads report the resolved head;
- writes use an owned non-protected ref, ordered operations, CAS, and a correct idempotency key;
- the resulting changeset or ref head is verified;
- shared changes use a proposal plus genuinely independent review and merge;
- OAuth, connectivity, identity, or schema limitations are reported precisely;
- no secrets, private chain-of-thought, or unrelated personal data were exposed.
