# atlas-action

A GitHub Action for [Atlas](https://github.com/ariga/atlas).

This action is used for [linting migration directories](https://atlasgo.io/versioned/lint)
to detect potentially dangerous changes to database schemas.

### Supported directory formats

This action supports analyzing migrations directories in formats
accepted by different schema migration tools: 
* [Atlas](https://atlasgo.io)
* [golang-migrate](https://github.com/golang-migrate/migrate)

### Analyzers
This action supports via Atlas the following analyzers: 
* Detection of destructive changes
* Detection of data-dependant changes
* Invalid SQL statements
* Migration directory integrity (order, history changes)

[Read more about Atlas analyzers](https://atlasgo.io/lint/analyzers)

### Usage

Add `.github/workflows/atlas-ci.yaml` to your repo with the following contents:

```yaml
name: Atlas CI 
on:
  # Run whenever code is changed in the master.
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
      # Spin up a mysql:8 container to be used as the dev-database for analysis. 
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
      - uses: ariga/atlas-action@master
        with:
          dir: path/to/migrations
          dev-db: mysql://root:pass@localhost:3306/test
```

### Configuration

Configure the action by passing input parameters in the `with:` block. 

#### `dir`

Sets the directory that contains the migration scripts to analyze. 

#### `dev-db`

The URL of the dev-database to use for analysis. 

* Read about [Atlas URL formats](https://atlasgo.io/concepts/url)
* Read about [dev-databases](https://atlasgo.io/concepts/dev-database)

#### `latest`

Use the `latest` mode to decide which files to analyze. By default,
Atlas will use `git-base` to analyze any files that are present in the
diff between the base branch and the current. 

The full list of input options can be found in [action.yml](action.yml).