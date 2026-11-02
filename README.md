# Nerds Who Fish Helm charts

Public Helm packages at [charts.nerdswhofish.com](https://charts.nerdswhofish.com).
Chart source stays with its application; this repository stores released packages and publishes the Helm index to Cloudflare R2.

```sh
helm repo add nerdswhofish https://charts.nerdswhofish.com
helm repo update
helm search repo nerdswhofish
```

| Chart | Source and installation |
| --- | --- |
| Fjord | [NerdsWhoFish/fjord](https://github.com/NerdsWhoFish/fjord/tree/main/charts/fjord) |

## Publish a chart

Register its name and source repository in `publishers.json`.
Its `Chart.yaml` must include that repository's HTTPS URL in `sources`.

The application release packages its chart, then calls this action:

```yaml
- run: helm package charts/fjord --destination artifacts
- uses: NerdsWhoFish/helm-charts/publish@main # Pin a reviewed commit in production.
  with:
    package: artifacts/fjord-1.0.0.tgz
    app-id: ${{ secrets.HELM_APP_ID }}
    app-private-key: ${{ secrets.HELM_APP_PRIVATE_KEY }}
```

The GitHub App must be installed on this repository with Contents write access.
The action mints a short-lived token restricted to this repository and revokes it at job completion.
Store the private key only in trusted source repositories.
The key's installation permissions can be broader than the minted token; publisher repositories are a trust boundary, not isolated chart tenants.

Packages are committed under `packages/<name>/`.
Reusing a version with different contents fails.
Repackaging identical contents preserves the original archive.
Concurrent publishers retry against the newest Git head.

## R2 setup

Configure these repository values:

| Kind | Name | Purpose |
| --- | --- | --- |
| Secret | `R2_ACCESS_KEY_ID` | Bucket-scoped Object Read & Write credential |
| Secret | `R2_SECRET_ACCESS_KEY` | Matching S3 secret |
| Variable | `R2_ENDPOINT` | `https://<account-id>.r2.cloudflarestorage.com` |
| Variable | `R2_BUCKET` | Public chart bucket name |

Connect `charts.nerdswhofish.com` to the bucket.
Keep `/index.yaml` uncached; archives are immutable and may be cached for a year.
R2 credentials belong only in this repository, never in application repositories.

`Sync charts to R2` runs after package changes or manually.
It validates archives, checks existing object hashes, uploads missing packages, then replaces the index.
It never deletes released packages.
Job logs contain operation status and package counts.
A failed sync can be rerun; an upload failure leaves the previous index available.
Run the manual workflow after rotating credentials or repairing access.

## Validate locally

Requires Go 1.27.3 and Helm 3.19.0 or later.

```sh
GOWORK=off go test -race ./...
GOWORK=off go run ./cmd/charts --output dist
helm repo index dist --url https://charts.nerdswhofish.com
```

`dist` must be empty before generating it.
CI also checks package immutability against the previous commit and runs `helm lint --strict`.
