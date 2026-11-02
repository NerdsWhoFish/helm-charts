#!/usr/bin/env bash
set -euo pipefail

: "${R2_ENDPOINT:?Set R2_ENDPOINT}"
: "${R2_BUCKET:?Set R2_BUCKET}"
: "${AWS_ACCESS_KEY_ID:?Set AWS_ACCESS_KEY_ID}"
: "${AWS_SECRET_ACCESS_KEY:?Set AWS_SECRET_ACCESS_KEY}"
test -f dist/index.yaml

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
count=0
for archive in dist/*.tgz; do
  test -f "$archive" || continue
  object="$(basename "$archive")"
  digest="$(sha256sum "$archive" | cut -d ' ' -f 1)"
  if aws --endpoint-url "$R2_ENDPOINT" s3api head-object \
    --bucket "$R2_BUCKET" --key "$object" > "$scratch/head.json" 2> "$scratch/error"; then
    prior="$(jq -r '.Metadata.sha256 // ""' "$scratch/head.json")"
    if test "$prior" != "$digest"; then
      echo "::error::R2 already contains different bytes or missing integrity metadata for $object."
      exit 1
    fi
  elif grep -qE '\(404\)|\(NoSuchKey\)|\(NotFound\)' "$scratch/error"; then
    aws --endpoint-url "$R2_ENDPOINT" s3 cp "$archive" "s3://$R2_BUCKET/$object" \
      --content-type application/gzip \
      --cache-control "public, max-age=31536000, immutable" \
      --metadata "sha256=$digest" --only-show-errors
  else
    echo "::error::Unable to inspect the destination object. Check R2 access and availability."
    exit 1
  fi
  count=$((count + 1))
done

aws --endpoint-url "$R2_ENDPOINT" s3 cp index.html "s3://$R2_BUCKET/index.html" \
  --content-type text/html --cache-control "no-cache, max-age=0, must-revalidate" \
  --only-show-errors

# Readers can only discover a version after its archive is available.
aws --endpoint-url "$R2_ENDPOINT" s3 cp dist/index.yaml "s3://$R2_BUCKET/index.yaml" \
  --content-type application/yaml --cache-control "no-cache, max-age=0, must-revalidate" \
  --only-show-errors
printf '{"event":"chart_sync","status":"complete","packages":%s}\n' "$count"
