---
name: New Release
about: Propose a new release
title: Release v0.x.0
labels: ''
assignees: ''

---

- [Introduction](#introduction)
- [Prerequisites](#prerequisites)
- [Release Process](#release-process)
- [Announce the Release](#announce-the-release)
- [Final Steps](#final-steps)

## Introduction

This document defines the process for releasing llm-d-inference-scheduler.

## Prerequisites

1. Permissions to push to the llm-d-inference-scheduler repository.

2. Set the required environment variables based on the expected release number:

   ```shell
   export MAJOR=0
   export MINOR=1
   export PATCH=0
   export REMOTE=origin
   ```

3. If creating a release candidate, set the release candidate number.

   ```shell
   export RC=1
   ```

## Release Process

1. If needed, clone the llm-d-inference-scheduler [repo].

   ```shell
   git clone -o ${REMOTE} git@github.com:llm-d/llm-d-inference-scheduler.git
   ```

2. If you already have the repo cloned, ensure it’s up-to-date and your local branch is clean.

3. Release Branch Handling:
   - For a Release Candidate:
     Create a new release branch from the `main` branch. The branch should be named `release-${MAJOR}.${MINOR}`, for example, `release-0.1`:

     ```shell
     git checkout -b release-${MAJOR}.${MINOR}
     ```

   - For a Major, Minor or Patch Release:
     A release branch should already exist. In this case, check out the existing branch:

     ```shell
     git checkout -b release-${MAJOR}.${MINOR} ${REMOTE}/release-${MAJOR}.${MINOR}
     ```

4. Update release-specific content, generate release artifacts, and stage the changes.

5. Sign, commit, and push the changes to the `llm-d-inference-scheduler` repo.

   For a release candidate:

    ```shell
    git commit -s -m "Updates artifacts for v${MAJOR}.${MINOR}.${PATCH}-rc.${RC} release"
    ```

   For a major, minor or patch release:

    ```shell
    git commit -s -m "Updates artifacts for v${MAJOR}.${MINOR}.${PATCH} release"
    ```

6. Push your release branch to the llm-d-inference-scheduler remote.

    ```shell
    git push ${REMOTE} release-${MAJOR}.${MINOR}
    ```

7. Tag the head of your release branch with the number.

   For a release candidate:

    ```shell
    git tag -s -a v${MAJOR}.${MINOR}.${PATCH}-rc.${RC} -m 'llm-d-inference-scheduler v${MAJOR}.${MINOR}.${PATCH}-rc.${RC} Release Candidate'
    ```

   For a major, minor or patch release:

    ```shell
    git tag -s -a v${MAJOR}.${MINOR}.${PATCH} -m 'llm-d-inference-scheduler v${MAJOR}.${MINOR}.${PATCH} Release'
    ```

8. Push the tag to the llm-d-inference-scheduler repo.

   For a release candidate:

    ```shell
    git push ${REMOTE} v${MAJOR}.${MINOR}.${PATCH}-rc.${RC}
    ```

   For a major, minor or patch release:

    ```shell
    git push ${REMOTE} v${MAJOR}.${MINOR}.${PATCH}
    ```

9. Pushing the tag triggers CI action to build and publish the container image to the [ghcr registry].
10. Test the steps in the tagged quickstart guide after the PR merges. TODO add e2e tests! <!-- link to an e2e tests once we have such one -->
11. Create a [new release]:
    1. Choose the tag that you created for the release.
    2. Use the tag as the release title, i.e. `v0.1.0` refer to previous release for the content of the release body.
    3. Click "Generate release notes" and preview the release body.
    4. Go to Gateway Inference Extension latest release and make sure to include the highlights in llm-d-inference-scheduler as well.
    5. If this is a release candidate, select the "This is a pre-release" checkbox.
12. If you find any bugs in this process, create an [issue].

## Announce the Release

Use the following steps to announce the release.

1. Send an announcement email to `llm-d-contributors@googlegroups.com` with the subject:

   ```shell
   [ANNOUNCE] llm-d-inference-scheduler v${MAJOR}.${MINOR}.${PATCH} is released
   ```

2. Add a link to the final release in this issue.

3. Close this issue.

[repo]: https://github.com/llm-d/llm-d-inference-scheduler
[ghcr registry]: https://github.com/llm-d/llm-d-inference-scheduler/pkgs/container/llm-d-inference-scheduler
[new release]: https://github.com/llm-d/llm-d-inference-scheduler/releases/new
[issue]: https://github.com/llm-d/llm-d-inference-scheduler/issues/new/choose
