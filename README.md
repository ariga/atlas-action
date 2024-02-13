# GitHub Actions for Atlas

This repository contains GitHub Actions for working with [Atlas](https://atlasgo.io).

> If you are looking for the old TypeScript-based action, please see the old [README](doc/typescript-action.md). 

To learn more about the recommended way to build workflows, read our guide on 
[Modern CI/CD for Databases](https://atlasgo.io/guides/modern-database-ci-cd).

## Actions

| Action                                                             | Description                                             |
|--------------------------------------------------------------------|---------------------------------------------------------|
| [ariga/setup-atlas](#arigasetup-atlas)                             | Setup the Atlas CLI and optionally login to Atlas Cloud |
| [ariga/atlas-action/migrate/push](#arigaatlas-actionmigratepush)   | Push migrations to Atlas Cloud                          |
| [ariga/atlas-action/migrate/lint](#arigaatlas-actionmigratelint)   | Lint migrations                                         |
| [ariga/atlas-action/migrate/apply](#arigaatlas-actionmigrateapply) | Apply migrations to a database                          |

## Examples

The Atlas GitHub Actions can be composed into workflows to create CI/CD pipelines for your database schema.
Workflows will normally begin with the `setup-atlas` action, which will install the Atlas CLI and optionally
login to Atlas Cloud. Followed by whatever actions you need to run, such as `migrate lint` or `migrate apply`.

### Pre-requisites

The following examples require you to have an Atlas Cloud account and a push an initial version of your
migration directory. 

To create an account, first download the Atlas CLI (on Linux/macOS):

```bash
curl -sSL https://atlasgo.io/install | sh
```
For more installation options, see the [documentation](https://atlasgo.io/getting-started#installation).

Then, create an account by running the following command and following the instructions:

```bash
atlas login
```

After logging in, push your migration directory to Atlas Cloud:

```bash
atlas migrate push --dev-url docker://mysql/8/dev --dir-name my-project
```

For a more detailed guide, see the [documentation](https://atlasgo.io/versioned/intro#pushing-migrations-to-atlas).

Finally, you will need an API token to use the Atlas GitHub Actions. To create a token, see 
the [docs](https://atlasgo.io/cloud/bots).

### Continuous Integration and Delivery

This example workflow shows how to configure a CI/CD pipeline for your database schema. The workflow will
verify the safety of your schema changes when in a pull request and push migrations to Atlas Cloud when
merged into the main branch.

#### Quick Setup: Using the `gh` CLI

If you have the [gh](https://cli.github.com/) CLI installed, you can use the following command to
setup a workflow for your repository:

```bash
gh extension install ariga/gh-atlas
gh auth refresh -s write:packages,workflow
gh atlas init-action
```

This will create a pull request with a workflow that will run `migrate lint` on pull requests and
`migrate push` on the main branch. You can customize the workflow by editing the generated
`.github/workflows/atlas-ci.yaml` file.

#### Manual Setup: Create a workflow

Create a new file named `.github/workflows/atlas.yaml` with the following contents:

```yaml
name: Atlas CI/CD
on:
  push:
    branches:
      - master # Use your main branch here.
  pull_request:
    paths:
      - 'migrations/*' # Use the path to your migration directory here.
# Permissions to write comments on the pull request.
permissions:
  contents: read
  pull-requests: write
jobs:
  atlas:
    services:
      # Spin up a mysql:8 container to be used as the dev-database for analysis.
      mysql:
        image: mysql:8
        env:
          MYSQL_DATABASE: dev
          MYSQL_ROOT_PASSWORD: pass
        ports:
          - 3306:3306
        options: >-
          --health-cmd "mysqladmin ping -ppass"
          --health-interval 10s
          --health-start-period 10s
          --health-timeout 5s
          --health-retries 10
    runs-on: ubuntu-latest
    env:
      GITHUB_TOKEN: ${{ github.token }}
    steps:
      - uses: actions/checkout@v3
        with:
          fetch-depth: 0
      - uses: ariga/setup-atlas@v0
        with:
          cloud-token: ${{ secrets.ATLAS_CLOUD_TOKEN }}
      - uses: ariga/atlas-action/migrate/lint@v1
        with:
          dir: 'file://migrations'
          dir-name: 'my-project' # The name of the project in Atlas Cloud
          dev-url: "mysql://root:pass@localhost:3306/dev"
      - uses: ariga/atlas-action/migrate/push@v1
        if: github.ref == 'refs/heads/master'
        with:
          dir: 'file://migrations'
          dir-name: 'my-project' 
          dev-url: 'mysql://root:pass@localhost:3306/dev' # Use the service name "mysql" as the hostname
```

This example uses a MySQL database, but you can use any database supported by Atlas.  
For more examples, see the [documentation](https://atlasgo.io/integrations/github-actions).

### Continuous Deployment

This example workflow shows how to configure a continuous deployment pipeline for your database schema. The workflow will
apply migrations on the target database whenever a new commit is pushed to the main branch.

```yaml
name: Atlas Continuous Deployment
on:
  push:
    branches:
      - master
jobs:
  apply:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
        with:
          fetch-depth: 0
      - uses: ariga/setup-atlas@v0
        with:
          cloud-token: ${{ secrets.ATLAS_CLOUD_TOKEN }}
      - uses: ariga/atlas-action/migrate/apply@v1
        with:
          url: 'mysql://user:${{ secrets.DB_PASSWORD }}@db.hostname.io:3306/db'
          dir: 'atlas://my-project' # A directory stored in Atlas Cloud, use ?tag=<tag> to specify a tag
```

This example workflow shows how to configure a deployment pipeline for your database schema. 
This workflow will pull the most recent version of your migration directory from Atlas Cloud
and apply it to the target database.

For more examples, see the [documentation](https://atlasgo.io/integrations/github-actions).

## API 

### `ariga/setup-atlas`

Setup the Atlas CLI and optionally login to Atlas Cloud.

#### Inputs
* `cloud-token` - (Optional) The Atlas Cloud token to use for authentication. To create
   a cloud token see the [docs](https://atlasgo.io/cloud/bots).
* `version` - (Optional) The version of the Atlas CLI to install.  Defaults to the latest
   version.

### `ariga/atlas-action/migrate/push` 

Push the current version of your migration directory to Atlas Cloud.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to push.  For example: `file://migrations`.
   Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - The name (slug) of the project in Atlas Cloud.  
* `dev-url` - The URL of the dev-database to use for analysis.  For example: `mysql://root:pass@localhost:3306/dev`.
   Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `tag` - The tag to apply to the pushed migration directory.  By default the current git commit hash is used.
* `config` - The path to the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.

#### Outputs

* `url` - The URL of the migration directory in Atlas Cloud, containing an ERD visualization of the schema.

### `ariga/atlas-action/migrate/lint`

Lint migration changes with Atlas

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to lint.  For example: `file://migrations`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - The name (slug) of the project in Atlas Cloud.
* `dev-url` - The URL of the dev-database to use for analysis.  For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `config` - The path to the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.

#### Outputs

* `url` - The URL of the CI report in Atlas Cloud, containing an ERD visualization 
   and analysis of the schema migrations.

### `ariga/atlas-action/migrate/apply`

Apply migrations to a database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `url` - The URL of the target database.  For example: `mysql://root:pass@localhost:3306/dev`.
* `dir` - The URL of the migration directory to apply.  For example: `atlas://dir-name` for cloud
   based directories or `file://migrations` for local ones.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects). 
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `dry-run` - Print SQL without executing it. Defaults to `false`

#### Outputs

* `current` - The current version of the database. (before applying migrations)
* `target` - The target version of the database.
* `pending_count` - The number of migrations that will be applied.
* `applied_count` - The number of migrations that were applied.

### Legal 

The source code for this GitHub Action is released under the Apache 2.0
License, see [LICENSE](LICENSE).

This action downloads a binary version of [Atlas](https://atlasgo.io) which
is distributed under the [Ariga EULA](https://ariga.io/legal/atlas/eula).
