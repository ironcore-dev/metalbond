# Workflows

Workflows listening to GitHub events are listed below.

## release

Runs only for releases and pushes to tags matching `v*` 

1. Build debian package
1. Pack debian sources in tarball `make tarball`
1. publish debian package to github releases
1. publish debian source tarball to gardenlinux repo
    - triggers gitlab pipeline defined [here](https://gitlab.com/gardenlinux/gardenlinux-metalbond)

For details see [release.yml](release.yml)

## test

Runs for all branches and pull requests.

For details see [test.yml](test.yml)


## golangci-lint

Runs for all pull requests

1. golangci-lint

For details see [golangci-lint.yml](golangci-lint.yml)


## release-drafter

Runs on pushes to master, and pull requests

For details see [release-drafter.yml](release-drafter.yml)

## size-label

Runs on pull reuests

For details see [size-label.yml](size-label.yml)


## Workflows in this directory, but not documented here

Helper workflows listen to the `workflow_call` event, and are only called via `uses` from one of the above.

