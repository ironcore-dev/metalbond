# Workflows

Workflows listening to GitHub events are listed below.

## release-drafter

Changes on master are detected, and added to the next draft release of metalbond.

For details see [release-drafter.yml](release-drafter.yml)

## release

Automatically runs if you publish a release.
You can publish a drafted release. 
Once you publish a draft releases, this action is triggered automatically and builds the 
debian packages, and attaches the build artifacts to the release. 

For details see [release.yml](release.yml)

## test

Runs for all branches and pull requests.

For details see [test.yml](test.yml)


## golangci-lint

Runs for all pull requests

1. golangci-lint

For details see [golangci-lint.yml](golangci-lint.yml)


## size-label

Runs on pull reuests, and labels the size of the PR. 

For details see [size-label.yml](size-label.yml)


## Workflows in this directory, but not documented here

Helper workflows listen to the `workflow_call` event, and are only called via `uses` from one of the above.

