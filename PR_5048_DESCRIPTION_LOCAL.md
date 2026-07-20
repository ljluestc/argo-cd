# feat: add Strimzi KafkaMirrorMaker CRD custom health checks (Fixes #5048)

Checklist:

* [x] Either (a) I've created an enhancement proposal and discussed it with the community, (b) this is a bug fix, or (c) this does not need to be in the release notes.
* [x] The title of the PR states what changed and the related issues number (used for the release note).
* [x] The title of the PR conforms to the Title of the PR guidance.
* [x] I've included "Closes [ISSUE #]" or "Fixes [ISSUE #]" in the description to automatically close the associated issue.
* [x] I've updated both the CLI and UI to expose my feature, or I plan to submit a second PR with them.
* [x] Does this PR require documentation updates?
* [x] I've updated documentation as required by this PR.
* [x] I have signed off all my commits as required by DCO.
* [x] I have written unit and/or e2e tests for my change. PRs without these are unlikely to be merged.
* [x] My build is green (troubleshooting builds).
* [x] My new feature complies with the feature status guidelines.
* [x] I have added a brief description of why this PR is necessary and/or what this PR solves.
* [ ] Optional. My organization is added to USERS.md.
* [ ] Optional. For bug fixes, I've indicated what older releases this fix should be cherry-picked into (this may or may not happen depending on risk/complexity).

Fixes #5048

## Why this PR is necessary

Argo CD currently has no custom health check for the Strimzi `KafkaMirrorMaker` custom resource.
Without this, a `KafkaMirrorMaker` object can be reported as `Healthy` before the underlying resources
(for example deployments and generated configurations) are actually ready.

That can break deployment ordering in environments that rely on Argo CD application dependencies and
sync waves, because downstream applications may be allowed to deploy too early.

## What this PR changes

This PR adds custom health checks for the Strimzi `KafkaMirrorMaker` CRD, following the same
design pattern already used for other Strimzi resources (for example the prior `#3684` approach).

The PR includes:

1. Lua health check logic for `KafkaMirrorMaker` that evaluates status conditions and readiness.
2. State mapping that correctly reports `Progressing`, `Healthy`, or `Degraded` depending on actual resource status.
3. Test cases that validate expected transitions and prevent regressions.

## Expected behavior after this change

- `KafkaMirrorMaker` resources are no longer marked `Healthy` prematurely.
- Argo CD can respect real readiness state for sync-wave ordering.
- Dependent applications can safely wait for `KafkaMirrorMaker` to reach a true healthy state.

## Testing

- Added/updated health check tests for `KafkaMirrorMaker`.
- Covered initial creation/provisioning, readiness, and failure/degraded scenarios.
- Verified the resource remains `Progressing` until required underlying components are actually ready.

## Documentation

This feature changes user-visible health reporting behavior for Strimzi `KafkaMirrorMaker` resources.
Any relevant operator-facing health check documentation is updated accordingly.

## Release note

Add custom health checks for Strimzi `KafkaMirrorMaker` resources so Argo CD reports accurate
`Progressing`/`Healthy`/`Degraded` states and improves sync-wave deployment ordering.
