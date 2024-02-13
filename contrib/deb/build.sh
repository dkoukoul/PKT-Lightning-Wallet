#!/bin/bash

#
# This script should be run from the project root
# e.g. ./contrib/deb/build.sh
#
set -e
fpm -n pkt-lightning-wallet-linux -s dir -t deb -v "$(./bin/pld --version | sed 's/.* version //' | tr -d '\n')" ./bin