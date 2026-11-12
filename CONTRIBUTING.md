# Contributing

## Build

### From source

```sh
npm install --prefix ui
npm run build --prefix ui
go build -o smsc -ldflags "-s -w -X github.com/ellanetworks/smsc/version.GitCommit=$(git rev-parse HEAD)" ./cmd/smsc
```

### Container Image

```sh
rockcraft pack
```

### Snap

```sh
snapcraft pack
```

### Documentation

```sh
uvx --with-requirements requirements-docs.txt mkdocs build --strict
```

## Run

```sh
./smsc --config smsc.yaml
```

## Test

```sh
sudo modprobe sctp
go test ./...
npm test --prefix ui
```

The UI end-to-end tests build the UI and the SMSC, and run it in the `ella-smsc:latest` image with Docker Compose. They need Docker and that image, and Playwright's Chromium:

```sh
npx --prefix ui playwright install chromium
npm run test:e2e --prefix ui
```

The cluster integration tests run 3-node clusters of the SMSC binary. The upgrade tests start the cluster on a previous version, built from the commit `previousRef` in `internal/integration/harness_linux_test.go`, and upgrade it to the working tree one node at a time. They need the full Git history:

```sh
go test -tags integration -count=1 -timeout 15m ./internal/integration
```

To upgrade from another version, set `SMSC_PREVIOUS_REF` to a commit or tag, or `SMSC_PREVIOUS_BINARY` to an SMSC binary. After each release, set `previousRef` to its tag, so that the changes that follow are tested against the upgrade users will run.
