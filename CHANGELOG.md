# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.1] - 2026-08-16

### Bug Fixes

- Handle rediscovery errors cleanly
- Address review feedback
- Gate changelog push on actual changes, quote release body
- Tag main branch images as latest in docker.yml
- Move request creation inside retry loop; remove redundant blank assignment

### CI

- Bump docker/build-push-action from 6 to 7
- Bump dependabot/fetch-metadata from 2 to 3
- Bump actions/checkout from 4 to 7
- Bump docker/setup-qemu-action from 3 to 4
- Bump docker/setup-buildx-action from 3 to 4
- Gate Docker build on successful tests

### Documentation

- Clarify config and state sources
- Clarify app key locking comment

### Features

- Add Hue setup UI and persisted app key
- Persist discovered bridge state
- Add git-cliff changelog generation on release
- Add 429-aware retry/backoff to HTTP client

### Miscellaneous

- Reuse Go checks in workflows
- Set explicit workflow permissions

### Plan

- Add Dependabot config with auto-merge

### Progress

- Add cert save flow and start UI hardening

### Refactor

- Unify config and runtime state yaml

### WIP

- Save context before UI redesign


