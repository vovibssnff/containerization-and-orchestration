#!/bin/sh
# Pick one connectivity break at random and apply it through the chart.
# The choice is written to scripts/.fault so the diagnosis can be checked
# afterwards. Look at Hubble before opening that file.
set -eu
cd "$(dirname "$0")/.."
faults="no-dns wrong-port wrong-label deny-postgres"
# shellcheck disable=SC2086
set -- $faults
n=$(awk 'BEGIN{srand(); print int(rand()*4)+1}')
eval "fault=\${$n}"
printf '%s\n' "$fault" > scripts/.fault
echo "applied a hidden fault (see Hubble, not scripts/.fault)"
helm upgrade shop deploy/charts/shop -n shop --set network.fault="$fault" --timeout 2m
