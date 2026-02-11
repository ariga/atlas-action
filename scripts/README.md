# Scripts

This directory contains shell scripts and their tests for the Atlas Action.

## Running Bats Tests

The tests are written using [Bats](https://github.com/bats-core/bats-core) (Bash Automated Testing System).

### On Linux

If you have `bats` installed locally:

```bash
bats scripts/*.bats
```

### On macOS or other non-Linux systems

Since the `shim.sh` script is designed for Linux, use Docker to run the tests in a Linux environment:

```bash
docker run --rm --entrypoint sh \
  -v $(pwd)/scripts:/scripts -w /scripts \
  arigaio/atlas:latest-alpine \
  -c "apk add --no-cache bash bats curl && bats *.bats"
```

To run tests without curl (some tests will be skipped):

```bash
docker run --rm --entrypoint sh \
  -v $(pwd)/scripts:/scripts -w /scripts \
  arigaio/atlas:latest-alpine \
  -c "apk add --no-cache bash bats && bats *.bats"
```

## Running shim.sh

The `shim.sh` script can be run directly or via curl:

```bash
# Direct execution with argument
./scripts/shim.sh migrate/lint

# Via curl (useful for CI environments)
curl -sSf https://raw.githubusercontent.com/ariga/atlas-action/master/scripts/shim.sh | sh -s -- migrate/lint

# With environment variables
curl -sSf https://raw.githubusercontent.com/ariga/atlas-action/master/scripts/shim.sh | ATLAS_ACTION_VERSION=v1 sh -s -- migrate/lint
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ATLAS_ACTION` | Action name (alternative to passing as argument) |
| `ATLAS_ACTION_VERSION` | Version to download (must start with `v`, e.g., `v1`) |
| `ATLAS_ACTION_LOCAL` | Set to `1` to use local `atlas-action` binary from PATH |
