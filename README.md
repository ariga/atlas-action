# atlas-action

A GitHub Action for [Atlas](https://github.com/ariga/atlas).

This action is used for [linting migration directories](https://atlasgo.io/versioned/lint)
using the `atlas migrate lint` command. This command  validates and analyzes the contents
of migration directories and generates insights and diagnostics on the selected changes:

* Ensure the migration history can be replayed from any point at time.
* Protect from unexpected history changes when concurrent migrations are written to the migration directory by
  multiple team members.
* Detect whether destructive or irreversible changes have been made or whether they are dependent on tables'
  contents and can cause a migration failure.

### Supported directory formats

This action supports analyzing migration directories in formats
accepted by different schema migration tools:
* [Atlas](https://atlasgo.io)
* [golang-migrate](https://github.com/golang-migrate/migrate)
* [goose](https://github.com/pressly/goose)
* [dbmate](https://github.com/amacneil/dbmate)
* [Flyway](https://flywaydb.org/)
* [Liquibase](https://www.liquibase.org/)

### Usage

Add `.github/workflows/atlas-ci.yaml` to your repo with the following contents:

```yaml
name: Atlas CI
on:
  # Run whenever code is changed in the master branch,
  # change this to your root branch.
  push:
    branches:
      - master
  # Run on PRs where something changed under the `path/to/migration/dir/` directory.
  pull_request:
    paths:
      - 'path/to/migration/dir/*'
# Permissions to write comments on the pull request.
permissions:
  contents: read
  pull-requests: write
jobs:
  lint:
    services:
      # Spin up a mysql:8.0.29 container to be used as the dev-database for analysis.
      # If you use a different database, change the image configuration and update
      # the `dev-url` configuration below.
      mysql:
        image: mysql:8.0.29
        env:
          MYSQL_ROOT_PASSWORD: pass
          MYSQL_DATABASE: test
        ports:
          - "3306:3306"
        options: >-
          --health-cmd "mysqladmin ping -ppass"
          --health-interval 10s
          --health-start-period 10s
          --health-timeout 5s
          --health-retries 10
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3.0.1
        with:
          fetch-depth: 0 # Mandatory unless "latest" is set below.
      - uses: ariga/atlas-action@v0
        with:
          dir: path/to/migrations
          dir-format: atlas
          dev-url: mysql://root:pass@localhost:3306/test
```

### Configuration

Configure the action by passing input parameters in the `with:` block.

#### `dir`

Sets the directory that contains the migration scripts to analyze.

#### `dir-format`

Sets the format of the migration directory. Options: `atlas` (default),
`golang-migrate`, `goose`, `dbmate`, `flyway`, or `liquibase`.

#### `dev-url`

The URL of the dev-database to use for analysis.

* Read about [Atlas URL formats](https://atlasgo.io/concepts/url)
* Read about [dev-databases](https://atlasgo.io/concepts/dev-database)

#### `latest`

Use the `latest` mode to decide which files to analyze. By default,
Atlas will use `git-base` to analyze any files that are present in the
diff between the base branch and the current.

Unless this option is set, the base branch (`master`/`main`/etc) must
be checked out locally or you will see an error such as:
```
Atlas failed with code 1: Error: git diff: exit status 128
```

### `cloud-token`

Connect the action to [Atlas Cloud](https://atlasgo.cloud/) to get access to more analyzers,
entity relationship diagrams (ERDs) of your schema, and full CI reports.
Generate the token from within Atlas Cloud by creating a CI Bot. Read the full tutorial
[here](https://atlasgo.io/cloud/getting-started#connecting-to-the-atlas-github-action).

![atlas-cloud](https://atlasgo.io/uploads/images/issues-found-ci.png)

The full list of input options can be found in [action.yml](action.yml).

### Legal

The source code for this GitHub Action is released under the Apache 2.0
License, see [LICENSE](LICENSE).

This action downloads a binary version of [Atlas](https://atlasgo.io) which
is distributed under the [Ariga EULA](https://ariga.io/legal/atlas/eula).
