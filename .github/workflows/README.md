# Workflows

Workflows listening to GitHub events are listed below.

## release-drafter

Changes on main are detected, and added to the next draft release of metalbond.

For details see [release-drafter.yml](release-drafter.yml)

## publish-docker

Builds and publishes the Docker image on pushes to main and on tags.

For details see [publish-docker.yml](publish-docker.yml)

## test

Runs for all branches and pull requests.

For details see [test.yml](test.yml)

## golangci-lint

Runs for all pull requests

1. golangci-lint

For details see [golangci-lint.yml](golangci-lint.yml)
