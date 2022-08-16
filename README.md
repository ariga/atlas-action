# atlas-action

A GitHub Action for [Atlas](https://github.com/ariga/atlas).

This action is used for [linting migration directories](https://atlasgo.io/versioned/lint)
using the `atlas migrate lint` command. This command  validates and analyzes the contents
of migration directories and generates insights and diagnostics on the selected changes:

* Ensure the migration history can be replayed from any point at time.
* Protect from unexpected history changes when concurrent migrations are written to the migration directory by 
  multiple team members. Read more about the consistency checks in the section below.
* Detect whether destructive or irreversible changes have been made or whether they are dependent on tables'  
  contents and can cause a migration failure.

### Supported directory formats

This action supports analyzing migration directories in formats
accepted by different schema migration tools: 
* [Atlas](https://atlasgo.io)
* [golang-migrate](https://github.com/golang-migrate/migrate)
* [goose](https://github.com/pressly/goose)
* [dbmate](https://github.com/amacneil/dbmate)

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
jobs:
  ent:
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
          - "3307:3306"
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3.0.1
        with:
          fetch-depth: 0 # Mandatory unless "latest" is set below.
      - uses: ariga/atlas-action@master
        with:
          dir: path/to/migrations
          dir-format: golang-migrate
          dev-url: mysql://root:pass@localhost:3307/test
```

### Configuration

Configure the action by passing input parameters in the `with:` block. 

#### `dir`

Sets the directory that contains the migration scripts to analyze. 

#### `dir-format`

Sets the format of the migration directory. Options: `atlas` (default),
`golang-migrate`, `goose` or `dbmate`. Coming soon: `flyway`, `liquibase`. 

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

The full list of input options can be found in [action.yml](action.yml).

### Legal

The source code for this GitHub Action is released under the Apache 2.0
License, see [LICENSE](LICENSE).

This action downloads a binary version of [Atlas](https://atlasgo.io) which
is distributed under the [Ariga EULA](https://ariga.io/legal/atlas/eula).
