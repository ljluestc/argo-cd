Suggested PR title:
feat(cli): include app history health in `argocd app get -o yaml` output (fixes #10244)

Fixes #10244

## What this PR does / why we need it
`argocd app get <app> -o yaml` currently returns `status.history` entries without health results. In failure scenarios (for example, one or more failed syncs while older pods still serve traffic), users cannot easily identify which historical deployment is healthy.

This PR adds health information to each history entry so operators can quickly determine the most recent known-good deployment directly from CLI output.

## Summary of your changes
- Extend history item output to include a health object per entry.
- Populate `status.history[].health.status` when health information is available for the historical deployment.
- Preserve backward compatibility when health data is unavailable by leaving the field empty/omitted according to existing serialization behavior.

## Example output
```yaml
status:
  history:
    - deployStartedAt: "2022-05-26T22:56:28Z"
      deployedAt: "2022-05-26T22:56:29Z"
      id: 0
      health:
        status: Degraded
      revision: <revision>
      source:
        path: .
        repoURL: <repo-url>
        targetRevision: <target-revision>
```

## Validation plan
- Add/adjust unit tests for history mapping/serialization to verify health is included when present.
- Add regression coverage to confirm no breakage when health is absent.
- Manual verification:
  - Run `argocd app get <app> -o yaml`.
  - Confirm `status.history[].health.status` appears for entries with available health data.

## Checklist
* [x] Either (a) I've created an enhancement proposal and discussed it with the community, (b) this is a bug fix, or (c) this does not need to be in the release notes.
* [x] The title of the PR states what changed and the related issues number (used for the release note).
* [x] The title of the PR conforms to the [Title of the PR](https://argo-cd.readthedocs.io/en/latest/developer-guide/submit-your-pr/#title-of-the-pr)
* [x] I've included "Closes [ISSUE #]" or "Fixes [ISSUE #]" in the description to automatically close the associated issue.
* [x] I've updated both the CLI and UI to expose my feature, or I plan to submit a second PR with them.
* [ ] Does this PR require documentation updates?
* [ ] I've updated documentation as required by this PR.
* [ ] I have signed off all my commits as required by [DCO](https://github.com/argoproj/argoproj/blob/master/community/CONTRIBUTING.md#legal)
* [ ] I have written unit and/or e2e tests for my change. PRs without these are unlikely to be merged.
* [ ] My build is green ([troubleshooting builds](https://argo-cd.readthedocs.io/en/latest/developer-guide/ci/)).
* [x] My new feature complies with the [feature status](https://github.com/argoproj/argoproj/blob/master/community/feature-status.md) guidelines.
* [x] I have added a brief description of why this PR is necessary and/or what this PR solves.
* [ ] Optional. My organization is added to USERS.md.
* [ ] Optional. For bug fixes, I've indicated what older releases this fix should be cherry-picked into (this may or may not happen depending on risk/complexity).

## Cherry-pick consideration
If accepted as a low-risk change, consider cherry-picking to supported release branches where issue #10244 is affecting users.
