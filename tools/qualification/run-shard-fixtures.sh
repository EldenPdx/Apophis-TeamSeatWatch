#!/bin/sh
set -eu
phase=${TSW_QUALIFICATION_PHASE:-metrics}
run_range() {
  i=$1
  last=$2
  pids=''
  while [ "$i" -le "$last" ]; do
    profile=$(printf 'fixture-%03d' "$i")
    TSW_QUALIFICATION_PROFILE="$profile" TSW_QUALIFICATION_RECIPE="/evidence/recipes/$profile.json" /qualification.test -test.run '^TestNativeQualificationCDP$' -test.v -test.timeout 120s >"/evidence/$phase-$profile.log" 2>&1 &
    pids="$pids $!"
    i=$((i + 1))
  done
  status=0
  for pid in $pids; do if ! wait "$pid"; then status=1; fi; done
  return "$status"
}

if [ "${TSW_QUALIFICATION_STORAGE_ONLY:-0}" = 1 ]; then
  start=1
  while [ "$start" -le 10 ]; do
    end=$((start + 4))
    run_range "$start" "$end"
    echo "initialized $end/10 profiles; active batch completed"
    start=$((end + 1))
  done
else
  count=${TSW_QUALIFICATION_CONCURRENCY:-1}
  run_range 1 "$count"
  echo "completed $count native profiles"
fi
