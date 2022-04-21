#!/bin/sh -eu

main() {
    local version
    version=$(git describe --always)
    docker build -t quay.io/rh-obulatov/quay-ci-app .
    docker push quay.io/rh-obulatov/quay-ci-app
    git checkout manifests
    kustomize edit set image "$(docker inspect --format='{{index .RepoDigests 0}}' quay.io/rh-obulatov/quay-ci-app)"
    if ! git diff-index --quiet HEAD; then
        git commit -a -m "Deploy quay-ci-app $version"
        git push -u origin manifests
    fi
    git checkout @{-1}
}

main
