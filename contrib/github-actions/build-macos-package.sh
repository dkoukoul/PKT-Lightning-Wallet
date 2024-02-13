#!/bin/bash

function build() {
  cd "${GITHUB_WORKSPACE}" || exit
  bash -x ./do

  cd "${GITHUB_WORKSPACE}" || exit
  bash -x ./contrib/macos/build.sh

  local VERSION
  VERSION=$(echo "${RELEASE_NAME}" | sed -E 's/.+-v//')

  mv -v "${GITHUB_WORKSPACE}"'/pkt-lightning-wallet-mac-'"${VERSION}"'.pkg' \
    "${GITHUB_WORKSPACE}"'/'"${RELEASE_NAME}"'-mac.pkg'
}
build