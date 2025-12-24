# GitHub Actions for Atlas

This repository contains GitHub Actions for working with [Atlas](https://atlasgo.io).

To learn more about the recommended way to build workflows, read our guide on 
[Modern CI/CD for Databases](https://atlasgo.io/guides/modern-database-ci-cd).

## Actions

| Action                                                                        | Description                                                                         |
|-------------------------------------------------------------------------------|-------------------------------------------------------------------------------------|
| [ariga/setup-atlas](#arigasetup-atlas)                                        | Setup the Atlas CLI and optionally login to Atlas Cloud                             |
| [ariga/atlas-action/migrate/push](#arigaatlas-actionmigratepush)              | Push migrations to [Atlas Registry](https://atlasgo.io/registry)                    |
| [ariga/atlas-action/migrate/lint](#arigaatlas-actionmigratelint)              | Lint migrations (required `atlas login` )                                           |
| [ariga/atlas-action/migrate/apply](#arigaatlas-actionmigrateapply)            | Apply migrations to a database                                                      |
| [ariga/atlas-action/migrate/down](#arigaatlas-actionmigratedown)              | Revert migrations to a database                                                     |
| [ariga/atlas-action/migrate/test](#arigaatlas-actionmigratetest)              | Test migrations on a database                                                       |
| [ariga/atlas-action/migrate/autorebase](#arigaatlas-actionmigrateautorebase)  | Fix `atlas.sum` conflicts in migration directory                                    |
| [ariga/atlas-action/migrate/hash](#arigaatlas-actionmigratehash)              | Fix `atlas.sum` if it is out of sync with the migration directory                   |
| [ariga/atlas-action/migrate/diff](#arigaatlas-actionmigratediff)              | Run Migrate diff and commit the changes to the migration directory                  |
| [ariga/atlas-action/schema/test](#arigaatlas-actionschematest)                | Test schema on a database                                                           |
| [ariga/atlas-action/schema/lint](#arigaatlas-actionschemalint)                | Lint database schema with Atlas                                                     |
| [ariga/atlas-action/schema/push](#arigaatlas-actionschemapush)                | Push a schema to [Atlas Registry](https://atlasgo.io/registry)                      |
| [ariga/atlas-action/schema/plan](#arigaatlas-actionschemaplan)                | Plan a declarative migration for a schema transition                                |
| [ariga/atlas-action/schema/plan/approve](#arigaatlas-actionschemaplanapprove) | Approve a declarative migration plan                                                |
| [ariga/atlas-action/schema/apply](#arigaatlas-actionschemaapply)              | Apply a declarative migrations to a database                                        |
| [ariga/atlas-action/monitor/schema](#arigaatlas-actionmonitorschema)          | Sync the database schema to [Atlas Cloud Monitoring](https://atlasgo.io/monitoring) |

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
          cloud-token: ${{ secrets.ATLAS_TOKEN }}
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
          cloud-token: ${{ secrets.ATLAS_TOKEN }}
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
* `version` - (Optional) The version of the Atlas CLI to install. Defaults to the latest
   version.
* `flavor` - (Optional) The driver flavor to install. Some drivers require customer binaries like ("snowflake", "spanner").

### `ariga/atlas-action/migrate/push` 

Push the current version of your migration directory to Atlas Cloud.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to push. For example: `file://migrations`.
   Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - The name (slug) of the project in Atlas Cloud.  
* `dev-url` - The URL of the dev-database to use for analysis.  For example: `mysql://root:pass@localhost:3306/dev`.
   Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `tag` - The tag to apply to the pushed migration directory.  By default the current git commit hash is used.
* `latest` - Whether to implicitly push the "latest" tag. True by default.
* `config` - The path to the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `url` - The URL of the migration directory in Atlas Cloud, containing an ERD visualization of the schema.

### `ariga/atlas-action/migrate/lint` (Required `atlas login`)

Lint migration changes with Atlas

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to lint. For example: `file://migrations`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - The name (slug) of the project in Atlas Cloud.
* `tag` - The tag of migrations to used as base for linting. By default, the `latest` tag is used.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `config` - The path to the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `url` - The URL of the CI report in Atlas Cloud, containing an ERD visualization 
   and analysis of the schema migrations.

### `ariga/atlas-action/migrate/apply`

Apply migrations to a database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `url` - The URL of the target database. For example: `mysql://root:pass@localhost:3306/prod`.
* `dir` - The URL of the migration directory to apply.  For example: `atlas://dir-name` for cloud
   based directories or `file://migrations` for local ones.
* `amount` - The maximum number of migration files to apply, default is all.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects). 
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `dry-run` - Print SQL without executing it. Defaults to `false`
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.
* `allow-dirty` - Allow applying migration on a non-clean database. Defaults to false.
* `tx-mode` - The transaction mode for migrations. It can be either `file`, `all`, or `none`. The default is `file`.

#### Outputs

* `current` - The current version of the database. (before applying migrations)
* `target` - The target version of the database.
* `pending_count` - The number of migrations that will be applied.
* `applied_count` - The number of migrations that were applied.

### `ariga/atlas-action/migrate/down`

Revert migrations to a database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `url` - The URL of the target database.  For example: `mysql://root:pass@localhost:3306/dev`.
* `dir` - The URL of the migration directory to apply.  For example: `atlas://dir-name` for cloud
   based directories or `file://migrations` for local ones.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects). 
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `amount` - The amount of applied migrations to revert, defaults to 1.
* `to-version` - To which version to revert.
* `to-tag` - To which tag to revert.
* `wait-timeout` - Time after which no other retry attempt is made and the action exits. If not set, only one attempt is made.
* `wait-interval` - Time in seconds between different migrate down attempts, useful when waiting for plan approval, defaults to 1s. 
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `current` - The current version of the database. (before applying migrations)
* `target` - The target version of the database.
* `pending_count` - The number of migrations that will be applied.
* `reverted_count` - The number of migrations that were reverted.
* `url` - The URL of the plan to review and approve / reject.

### `ariga/atlas-action/migrate/test`

Run schema migration tests. Read more in [Atlas website](https://atlasgo.io/testing/migrate).

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `dir` - The URL of the migration directory to apply.  For example: `atlas://dir-name` for cloud
   based directories or `file://migrations` for local ones.
* `run` - Filter tests to run by regexp. For example, `^test_.*` will only run tests that start with `test_`.
  Default is to run all tests.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects). 
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.


### `ariga/atlas-action/migrate/autorebase`

Automatically resolves `atlas.sum` conflicts and rebases the migration directory onto the target branch. 

> **Note**
> 
> Users should set the `migrate/lint` action to ensure no logical conflicts occur after this action.
> 
> After the rebase is done and a commit is pushed by the action, no other workflows will be triggered unless the action is running with a personal access token (PAT).
>```
>   - uses: actions/checkout@v4
>     with:
>       token: ${{ secrets.PAT }}
>```


#### Inputs

All inputs are optional

* `base-branch` - The branch to rebase on. Defaults to repository's default branch.
* `remote` - The remote to fetch from. Defaults to `origin`.
* `dir` - The URL of the migration directory to rebase on. Defaults to `file://migrations`.
* `working-directory` - The working directory to run from. Defaults to project root.

#### Example usage

Add the next job to your workflow to automatically rebase migrations on top of the migration directory in case of conflicts:

```yaml
name: Rebase Atlas Migrations
on:
  # Run on push event and not pull request because github action does not run when there is a conflict in the PR.
  push:
    branches-ignore:
      - master
jobs:
  migrate-auto-rebase:
    permissions:
      # Allow pushing changes to repo
      contents: write
    runs-on: ubuntu-latest
    steps:
    - uses: ariga/setup-atlas@v0
      with:
        cloud-token: ${{ secrets.ATLAS_TOKEN }}
    - uses: actions/checkout@v4
      with:
        # Use personal access token to trigger workflows after commits are pushed by the action.
        token: ${{ secrets.PAT }}
        # Need to fetch the branch history for rebase.
        fetch-depth: 0
    # Skip the step below if your CI is already configured with a Git account.
    - name: config git to commit changes
      run: |
        git config --local user.email "github-actions[bot]@users.noreply.github.com"
        git config --local user.name "github-actions[bot]"
    - uses: ariga/atlas-action/migrate/autorebase@v1
      with:
        base-branch: master
        dir: file://migrations
```

### `ariga/atlas-action/migrate/hash`

Automatically resolves `atlas.sum` out of sync issues by re-generating the `atlas.sum` file based on the current state of the migration directory.

> **Note**
> 
> After the rehash is done and a commit is pushed by the action, no other workflows will be triggered unless the action is running with a personal access token (PAT).
>```
>   - uses: actions/checkout@v4
>     with:
>       token: ${{ secrets.PAT }}
>```

#### Inputs

All inputs are optional

* `base-branch` - The branch to pull from. Defaults to repository's default branch.
* `remote` - The remote to fetch from. Defaults to `origin`.
* `dir` - The URL of the migration directory to hash. Defaults to `file://migrations`.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.

#### Example usage

Add the next job to your workflow to automatically re-generate the `atlas.sum` file in case it is out of sync with the migration directory:

```yaml
name: Hash Atlas Migrations
on:
  pull_request:
jobs:
  migrate-hash:
    permissions:
      # Allow pushing changes to repo
      contents: write
    runs-on: ubuntu-latest
    steps:
    - uses: ariga/setup-atlas@v0
      with:
        cloud-token: ${{ secrets.ATLAS_TOKEN }}
    - uses: actions/checkout@v4
      with:
        # Use personal access token to trigger workflows after commits are pushed by the action.
        token: ${{ secrets.PAT }}
        fetch-depth: 0
    # Skip the step below if your CI is already configured with a Git account.
    - name: config git to commit changes
      run: |
        git config --local user.email "github-actions[bot]@users.noreply.github.com"
        git config --local user.name "github-actions[bot]"
    - uses: ariga/atlas-action/migrate/hash@v1
      with:
        dir: file://migrations
```

### `ariga/atlas-action/migrate/diff`

Automatically generate versioned migrations whenever the schema is changed, and commit them to the migration directory.
> **Note**
>
> After committing the changes to the migration directory, no other workflows will be triggered unless the action is run with a personal access token (PAT).
>```
>   - uses: actions/checkout@v4
>     with:
>       token: ${{ secrets.PAT }}
>```

#### Inputs

`dir`, `to`  and `dev-url` are required, but they can be specified in the Atlas configuration file via `config` and `env`.

* `dir` - The URL of the migration directory. For example: `file://migrations`.
* `to` - The URL of the desired schema state to transition to. For example: `file://schema.hcl`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `config` - The path to the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.
* `remote` - The remote to fetch from. Defaults to `origin`.

#### Example usage

```yaml
jobs:
  migrate-diff:
    permissions:
      # Allow pushing changes to repo and comments on the pull request
      contents: write
      pull-requests: write
    env:
      GITHUB_TOKEN: ${{ github.token }}
    runs-on: ubuntu-latest
    steps:
    - uses: ariga/setup-atlas@v0
      with:
        cloud-token: ${{ secrets.ATLAS_TOKEN }}
    - uses: actions/checkout@v4
      with:
        # Use personal access token to trigger workflows after commits are pushed by the action.
        token: ${{ secrets.PAT }}
        fetch-depth: 0
    # Skip the step below if your CI is already configured with a Git account.
    - name: config git to commit changes
      run: |
        git config --local user.email "github-actions[bot]@users.noreply.github.com"
        git config --local user.name "github-actions[bot]"
    - uses: ariga/atlas-action/migrate/diff@v1
      with:
        dev-url: "mysql://root:pass@localhost:3306/dev"
        dir: file://migrations
        to:  file://schema.sql # The desired schema state to transition to.
```

### `ariga/atlas-action/schema/test`

Run schema tests on the desired schema. Read more in [Atlas website](https://atlasgo.io/testing/schema).

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `url` - The desired schema URL(s) to test. For Example: `file://schema.hcl`
* `run` - Filter tests to run by regexp. For example, `^test_.*` will only run tests that start with `test_`.
  Default is to run all tests.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects). 
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
   For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

### `ariga/atlas-action/schema/apply`

Apply a declarative migrations to a database.

#### Inputs

* `to` - The URL(s) of the desired schema state.
* `url` - The URL of the target database. For example: `mysql://root:pass@localhost:3306/prod`.
* `plan` - Optional plan file to use for applying the migrations. For example: `atlas://<schema>/plans/<id>`.
* `dry-run` - Print SQL (and optional analysis) without executing it. Either `true` or `false`. Defaults to `false`.
* `auto-approve` - Automatically approve and apply changes. Either `true` or `false`. Defaults to `false`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `schema` - The database schema(s). For example: `public`.
* `include` - list of glob patterns used to select which resources to keep in inspection. see: https://atlasgo.io/declarative/inspect#include-schemas-
* `exclude` - list of glob patterns used to filter resources from applying. see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
  For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.
* `tx-mode` - The transaction mode for migrations. It can be either `file`, `all`, or `none`. The default is `file`.
* `lint-review` - Specifies the approval policy for applying migrations. It can be set to either `ALWAYS`, `ERROR` or `WARNING`. If set, the action will automatically create a plan if one does not exist and wait for approval. Note: This option is only available for Atlas Cloud users and won't available when `auto-approve`, `plan` or `dry-run` is set.
* `wait-timeout` - Time after which no other retry attempt is made and the action exits. If not set, only one attempt is made. Used when `lint-review` is set.
* `wait-interval` - Time in seconds between different apply attempts, useful when waiting for plan approval, defaults to 1s. Used when `lint-review` is set.

#### Outputs

* `error` - The error message if the action fails.

### `ariga/atlas-action/schema/lint`

Lint database schema with Atlas.

#### Inputs

* `url` - (Required) Schema URL(s) to lint. For example: `file://schema.hcl`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dev-url` - (Required) The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `schema` - (Optional) The database schema(s) to include. For example: `public`.
* `config` - (Optional) The path to the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - (Optional) The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - (Optional) Stringify JSON object containing variables to be used inside the Atlas configuration file.
  For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - (Optional) The working directory to run from. Defaults to project root.

### `ariga/atlas-action/schema/push`

Push a schema to [Atlas Registry](https://atlasgo.io/registry) with an optional tag.

#### Inputs

* `schema-name` - The name (slug) of the schema repository in [Atlas Registry](https://atlasgo.io/registry).
* `url` - Desired schema URL(s) to push. For example: `file://schema.hcl`.
* `tag` - The tag to apply to the pushed schema. By default, the current git commit hash is used.
* `latest` - Whether to implicitly push the `latest` tag. True by default.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `schema` - The database schema(s) to push. For example: `public`.
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
  For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `slug` - The slug of the schema repository in [Atlas Registry](https://atlasgo.io/registry).
* `link` - The URL of the schema in [Atlas Registry](https://atlasgo.io/registry).
* `url` - The URL of the pushed schema version in Atlas format. For example, `atlas://app`.

### `ariga/atlas-action/schema/plan`

Plan a declarative migration for a schema transition.

#### Inputs

* `schema-name` - The name (slug) of the schema repository in [Atlas Registry](https://atlasgo.io/registry).
* `from` - URL(s) of the current schema state to transition from.
* `to` - URL(s) of the desired schema state to transition to.
* `name` - Optional name for the plan. If not provided, a default plan is generated by Atlas.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `schema` - The database schema(s). For example: `public`.
* `include` - list of glob patterns used to select which resources to keep in inspection. see: https://atlasgo.io/declarative/inspect#include-schemas-
* `exclude` - list of glob patterns used to filter resources from applying. see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
  For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `plan` - The URL of the generated plan in Atlas format. For example, `atlas://app/plans/123`.
* `link` - The URL of the plan in [Atlas Registry](https://atlasgo.io/registry).
* `status` - The status of the plan. For example, `PENDING` or `APPROVED`.

### `ariga/atlas-action/schema/plan/approve`

Approve a declarative migration plan.

#### Inputs

* `schema-name` - The name (slug) of the schema repository in [Atlas Registry](https://atlasgo.io/registry).
* `from` - URL(s) of the current schema state to transition from.
* `to` - URL(s) of the desired schema state to transition to.
* `schema` - The database schema(s). For example: `public`.
* `include` - list of glob patterns used to select which resources to keep in inspection. see: https://atlasgo.io/declarative/inspect#include-schemas-
* `exclude` - list of glob patterns used to filter resources from applying. see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `plan` - Optional URL of the plan to be approved. For example, `atlas://<schema>/plans/<id>`. By default, Atlas
  searches in the registry for a plan corresponding to the given schema transition and approves it (typically, this plan
  is created during the PR stage). If multiple plans are found, an error will be thrown.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).
* `config` - The URL of the Atlas configuration file.  By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.  For example, `dev`.
* `vars` - Stringify JSON object containing variables to be used inside the Atlas configuration file.
  For example: `'{"var1": "value1", "var2": "value2"}'`.
* `working-directory` - The working directory to run from.  Defaults to project root.

#### Outputs

* `plan` - The URL of the generated plan in Atlas format. For example, `atlas://app/plans/123`.
* `link` - The URL of the plan in [Atlas Registry](https://atlasgo.io/registry).
* `status` - The status of the plan. For example, `PENDING` or `APPROVED`.

### `ariga/atlas-action/monitor/schema`

Monitor changes of the database schema and track them in Atlas Cloud.
Can be used periodically to [monitor](https://atlasgo.io/monitoring) changes in the database schema.

#### Inputs

* `cloud-token` - (required) The Atlas Cloud token to use for authentication. To create
  a cloud token see the [docs](https://atlasgo.io/cloud/bots).
* `url` - (optional) The URL of the database to monitor. For example: `mysql://root:pass@localhost:3306/prod` (mutually exclusive with `config` and `env`).
* `config` - (optional) The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl` (mutually exclusive with `url`).
* `env` - (optional) The environment to use from the Atlas configuration file. For example, `dev` (mutually exclusive with `url`).
* `slug` - (optional) Unique identifier for the database server.
* `schemas` - (optional) List of database schemas to include (by default includes all schemas). see: https://atlasgo.io/declarative/inspect#inspect-multiple-schemas.
* `exclude` - (optional) List of exclude patterns from inspection. see: https://atlasgo.io/declarative/inspect#exclude-schemas.
* `include` - (optional) List of include patterns for inspection. see: https://atlasgo.io/declarative/inspect#include-schemas-.
* `collect-stats` - (optional) Whether to collect schema statistics. Defaults to `true`. see: https://atlasgo.io/monitoring/statistics.

#### Outputs

* `url` - URL of the schema of the database inside Atlas Cloud.

#### Example usage

The following action will monitor changes to the `auth` and `app` schemas inside the `mysql://root:pass@localhost:3306` database and track them in Atlas Cloud.
In case the database URL is subject to change, the `slug` parameter can use to identify the same database across runs.

```yaml
        uses: ariga/atlas-action/monitor/schema@v1
        with:
          cloud-token: ${{ secrets.ATLAS_CLOUD_TOKEN }}
          url: 'mysql://root:pass@localhost:3306'
          schemas: |-
            auth
            app
```

### `ariga/atlas-action/setup`

This action builds the binary of atlas-action on your pipeline, instead of downloading it from the internet. So you can pin it and other actions to specified commit.

#### Inputs

* `token` - (Optional) The GitHub Token for GitHub Enterprise usage only.

#### Example usage

The following example demonstrates how to pin the `ariga/atlas-action/setup` action to a specific commit for better security and reproducibility:

```yaml
name: Setup Atlas CLI
on:
  push:
    branches:
      - main
jobs:
  migrate-apply:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout code
        uses: actions/checkout@v3
      # This action builds the binary locally
      - uses: ariga/atlas-action/setup@<commit-sha>
      # Pin other actions without `atlas-action/setup` won't work
      - uses: ariga/atlas-action/migrate/apply@<commit-sha>
        with:
          url: 'mysql://user:${{ secrets.DB_PASSWORD }}@db.hostname.io:3306/db'
          dir: 'atlas://my-project' # A directory stored in Atlas Cloud, use ?tag=<tag> to specify a tag
```

Replace `<commit-sha>` with the specific commit hash you want to pin the action to. Pinning to a commit ensures that the action's behavior does not change unexpectedly due to updates in the repository.

### Why Pin Actions to a Commit?

Pinning actions to a specific commit provides the following benefits:
- **Security**: Prevents the action from being modified by unauthorized changes in the repository.
- **Reproducibility**: Ensures that workflows behave consistently across runs, even if the action is updated in the future.

For more details, see the [GitHub Actions security best practices](https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions#using-third-party-actions).

### Development

To release the new version of atlas-action, bump the version in `VERSION.txt` and open the Pull Request to the master branch.

### Legal

The source code for this GitHub Action is released under the Apache 2.0
License, see [LICENSE](LICENSE).

This action downloads a binary version of [Atlas](https://atlasgo.io) which
is distributed under the [Ariga EULA](https://ariga.io/legal/atlas/eula).
