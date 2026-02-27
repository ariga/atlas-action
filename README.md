# GitHub Actions for Atlas

This repository contains GitHub Actions for working with [Atlas](https://atlasgo.io).

To learn more about the recommended way to build workflows, read our guide on 
[Modern CI/CD for Databases](https://atlasgo.io/guides/modern-database-ci-cd).

## Actions

| Action                                                                        | Description                                                                         |
|-------------------------------------------------------------------------------|-------------------------------------------------------------------------------------|
| [ariga/setup-atlas](#arigasetup-atlas)                                        | Setup the Atlas CLI and optionally login to Atlas Cloud                             |
| [ariga/atlas-action/copilot](#arigaatlas-actioncopilot) | Talk to Atlas Copilot. |
| [ariga/atlas-action/migrate/apply](#arigaatlas-actionmigrateapply) | Applies a migration directory on a target database |
| [ariga/atlas-action/migrate/autorebase](#arigaatlas-actionmigrateautorebase) | Automatically resolves `atlas.sum` conflicts and rebases the migration directory onto the target branch. |
| [ariga/atlas-action/migrate/hash](#arigaatlas-actionmigratehash) | Automatically generate a hash of the schema migrations directory, and commit it to the migration directory. |
| [ariga/atlas-action/migrate/diff](#arigaatlas-actionmigratediff) | Automatically generate versioned migrations whenever the schema is changed, and commit them to the migration directory. |
| [ariga/atlas-action/migrate/down](#arigaatlas-actionmigratedown) | Reverts deployed migration files on a target database |
| [ariga/atlas-action/migrate/lint](#arigaatlas-actionmigratelint) | CI for database schema changes with Atlas |
| [ariga/atlas-action/migrate/push](#arigaatlas-actionmigratepush) | Push the current version of your migration directory to Atlas Cloud. |
| [ariga/atlas-action/migrate/set](#arigaatlas-actionmigrateset) | Edits the revision table to consider all migrations up to and including the given version to be applied. |
| [ariga/atlas-action/migrate/test](#arigaatlas-actionmigratetest) | CI for database schema changes with Atlas |
| [ariga/atlas-action/monitor/schema](#arigaatlas-actionmonitorschema) | Sync the database schema to Atlas Cloud. |
| [ariga/atlas-action/schema/apply](#arigaatlas-actionschemaapply) | Applies schema changes to a target database |
| [ariga/atlas-action/schema/lint](#arigaatlas-actionschemalint) | Lint database schema with Atlas |
| [ariga/atlas-action/schema/plan](#arigaatlas-actionschemaplan) | Plan a declarative migration to move from the current state to the desired state |
| [ariga/atlas-action/schema/plan/approve](#arigaatlas-actionschemaplanapprove) | Approve a migration plan by its URL |
| [ariga/atlas-action/schema/push](#arigaatlas-actionschemapush) | Push a schema version with an optional tag to Atlas |
| [ariga/atlas-action/schema/test](#arigaatlas-actionschematest) | Run schema tests against the desired schema |

## Examples

The Atlas GitHub Actions can be composed into workflows to create CI/CD pipelines for your database schema.
Workflows will normally begin with the `setup-atlas` action, which will install the Atlas CLI and optionally
login to Atlas Cloud. Followed by whatever actions you need to run, such as `migrate lint` or `migrate apply`.

### Pre-requisites

The following examples require you to have an Atlas Cloud account and push an initial version of your
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
* `flavor` - (Optional) The driver flavor to install. Some drivers require custom binaries like ("snowflake", "spanner").

### `ariga/atlas-action/migrate/push`

Push the current version of your migration directory to Atlas Cloud.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to push. For example: `file://migrations`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - The name (slug) of the project in Atlas Cloud.
* `latest` - If true, push also to the `latest` tag.
* `revisions-schema` - The name of the schema containing the revisions table.
* `tag` - The tag to apply to the pushed migration directory. By default the
  current git commit hash is used.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

### `ariga/atlas-action/migrate/lint`

Lint migration changes with Atlas

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to lint. For example: `file://migrations`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `dir-name` - (Required) The name (slug) of the project in Atlas Cloud.
* `git-base` - The base branch to detected changes from.
* `git-dir` - The URL of the git directory to push to. Defaults to the current working directory.
* `revisions-schema` - The name of the schema containing the revisions table.
* `tag` - The tag of migrations to used as base for linting. By default, the `latest` tag is used.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

#### Outputs

* `url` - The URL of the CI report in Atlas Cloud, containing an ERD visualization 
  and analysis of the schema migrations.

### `ariga/atlas-action/migrate/apply`

Apply migrations to a database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `allow-dirty` - Allow working on a non-clean database.
* `amount` - The maximum number of migration files to apply. Default is all.
* `dir` - The URL of the migration directory to apply. For example: `atlas://dir-name` for cloud
  based directories or `file://migrations` for local ones.
* `dry-run` - Print SQL without executing it. Either "true" or "false".
* `exec-order` - How Atlas computes and executes pending migration files to the database.
  Either "linear", "linear-skip", or "non-linear". Learn more about [execution order](https://atlasgo.io/versioned/apply#execution-order).
* `revisions-schema` - The name of the schema containing the revisions table.
* `to-version` - The target version to apply migrations to. Mutually exclusive with `amount`.
* `tx-mode` - Transaction mode to use. Either "file", "all", or "none".
* `url` - The URL of the target database. For example: `mysql://root:pass@localhost:3306/dev`.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.

#### Outputs

* `applied_count` - The number of migrations that were applied.
* `current` - The current version of the database. (before applying migrations)
* `pending_count` - The number of migrations that will be applied.
* `runs` - A JSON array of objects containing the current version, target version,
  applied count, and pending count for each migration run.
* `target` - The target version of the database.

### `ariga/atlas-action/migrate/down`

Revert migrations to a database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `amount` - The amount of applied migrations to revert. Mutually exclusive with `to-tag` and `to-version`.
* `dir` - The URL of the migration directory to apply. For example: `atlas://dir-name` for cloud
  based directories or `file://migrations` for local ones.
* `revisions-schema` - The name of the schema containing the revisions table.
* `to-tag` - The tag to revert to. Mutually exclusive with `amount` and `to-version`.
* `to-version` - The version to revert to. Mutually exclusive with `amount` and `to-tag`.
* `url` - The URL of the target database. For example: `mysql://root:pass@localhost:3306/dev`.
* `wait-interval` - Time in seconds between different migrate down attempts.
* `wait-timeout` - Time after which no other retry attempt is made and the action exits.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

#### Outputs

* `current` - The current version of the database. (before applying migrations)
* `planned_count` - The number of migrations that will be applied.
* `reverted_count` - The number of migrations that were reverted.
* `target` - The target version of the database.
* `url` - If given, the URL for reviewing the revert plan.

### `ariga/atlas-action/migrate/test`

Run schema migration tests. Read more in [Atlas website](https://atlasgo.io/testing/migrate).

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory to apply. For example: `atlas://dir-name` for cloud
  based directories or `file://migrations` for local ones.
* `paths` - List of directories containing test files.
* `revisions-schema` - The name of the schema containing the revisions table.
* `run` - Filter tests to run by regexp.
  For example, `^test_.*` will only run tests that start with `test_`.
  Default is to run all tests.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

### `ariga/atlas-action/migrate/set`

Edits the revision table to consider all migrations up to and including the given version to be applied.
This command is usually used after manually making changes to the managed database.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory. For example: `file://migrations`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `revisions-schema` - The name of the schema containing the revisions table.
* `url` - The URL of the target database. For example: `mysql://root:pass@localhost:3306/dev`.
* `version` - (Required) The version to set the revision table to. All migrations up to and including this version
  will be marked as applied.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.

#### Example usage

```yaml
- uses: ariga/atlas-action/migrate/set@v1
  with:
    url: 'mysql://root:pass@localhost:3306/dev'
    dir: 'file://migrations'
    version: '20230922132634'
```

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

* `base-branch` - The base branch to rebase the migration directory onto. Default to the default branch of the repository.
* `dir` - The URL of the migration directory to rebase on. By default: `file://migrations`.
* `remote` - The remote to fetch from. Defaults to `origin`.
* `force-rebase` - When true, skip the merge step and rebase whenever there are dev-only migrations (e.g. out-of-order workflow after merging base and running apply with exec-order non-linear). Default is false.
* `working-directory` - Atlas working directory. Default is project root

#### Outputs

* `rebased` - Whether migration files were rebased. Either "true" or "false".
* `latest_version` - The latest migration version in the directory after rebase.

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

For out-of-order migrations (e.g. [git-flow hotfix resolution](https://atlasgo.io/faq/out-of-order-migrations)), run apply → autorebase with `force-rebase: true` → set only when main was merged in and the migration dir changed. Use a detect step so the job only runs when needed:

```yaml
name: Out-of-order migrations
on:
  pull_request:
    branches: [develop]
  workflow_dispatch:

env:
  MIGRATIONS_DIR: migrations

jobs:
  migrate:
    runs-on: ubuntu-latest
    services:
      mysql:
        image: mysql:8
        env:
          MYSQL_ROOT_PASSWORD: root
          MYSQL_DATABASE: app
        ports:
          - 3306:3306
        options: --health-cmd="mysqladmin ping" --health-interval=10s --health-timeout=5s --health-retries=3

    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Detect out-of-order migrations
        id: detect
        run: |
          need_rebase=false
          git fetch origin main develop
          if git merge-base --is-ancestor origin/main HEAD 2>/dev/null; then
            if [ -n "$(git diff --name-only origin/develop HEAD -- ${{ env.MIGRATIONS_DIR }}/)" ]; then
              need_rebase=true
              echo "need_rebase=true" >> "$GITHUB_OUTPUT"
            fi
          fi
          if [ "$need_rebase" = "true" ]; then
            echo "Base is develop, main is merged in, and migrations dir changed → running apply → rebase → set."
          else
            echo "Skipping: need base=develop, main merged in, and changes in ${{ env.MIGRATIONS_DIR }}/."
          fi

      - name: Install Atlas
        if: steps.detect.outputs.need_rebase == 'true'
        uses: ariga/setup-atlas@v0
        with:
          version: v0.29.1

      - name: Apply migrations (non-linear)
        if: steps.detect.outputs.need_rebase == 'true'
        uses: ariga/atlas-action/migrate/apply@v1
        with:
          dir: file://${{ env.MIGRATIONS_DIR }}
          url: mysql://root:root@localhost:3306/app
          exec-order: non-linear

      - name: Rebase migrations
        if: steps.detect.outputs.need_rebase == 'true'
        id: rebase
        uses: ariga/atlas-action/migrate/autorebase@v1
        with:
          dir: file://${{ env.MIGRATIONS_DIR }}
          base-branch: main
          force-rebase: true

      - name: Set migration version
        if: steps.detect.outputs.need_rebase == 'true'
        uses: ariga/atlas-action/migrate/set@v1
        with:
          dir: file://${{ env.MIGRATIONS_DIR }}
          url: mysql://root:root@localhost:3306/app
          version: ${{ steps.rebase.outputs.latest_version }}
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

* `base-branch` - The base branch to rebase the migration directory onto. Default to the default branch of the repository.
* `dir` - The URL of the migration directory to hash. By default: `file://migrations`.
* `remote` - The remote to fetch from. Defaults to `origin`.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.

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
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `remote` - The remote to push changes to. Defaults to `origin`.
* `to` - The URL of the desired state.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

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

* `paths` - List of directories containing test files.
* `run` - Filter tests to run by regexp.
  For example, `^test_.*` will only run tests that start with `test_`.
  Default is to run all tests.
* `url` - The desired schema URL(s) to test
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

### `ariga/atlas-action/schema/apply`

Apply a declarative migrations to a database.

#### Inputs

* `auto-approve` - Automatically approve and apply changes. Either "true" or "false".
* `dry-run` - Print SQL without executing it. Either "true" or "false".
* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `lint-review` - Automatically generate an approval plan before applying changes. Options are "ALWAYS", "ERROR" or "WARNING".
  Use "ALWAYS" to generate a plan for every apply, or "WARNING" and "ERROR" to generate a plan only based on review policy.
* `plan` - The plan to apply. For example, `atlas://<schema>/plans/<id>`.
* `schema` - List of database schema(s). For example: `public`.
* `to` - URL(s) of the desired schema state.
* `tx-mode` - Transaction mode to use. Either "file", "all", or "none".
* `url` - The URL of the target database to apply changes to.
  For example: `mysql://root:pass@localhost:3306/prod`.
* `wait-interval` - Time in seconds between different apply attempts.
* `wait-timeout` - Time after which no other retry attempt is made and the action exits.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

#### Outputs

* `error` - The error message if the action fails.

### `ariga/atlas-action/schema/lint`

Lint database schema with Atlas.

#### Inputs

* `schema` - The database schema(s) to include. For example: `public`.
* `url` - Schema URL(s) to lint. For example: `file://schema.hcl`.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

### `ariga/atlas-action/schema/push`

Push a schema to [Atlas Registry](https://atlasgo.io/registry) with an optional tag.

#### Inputs

* `description` - The description of the schema.
* `latest` - If true, push also to the `latest` tag.
* `schema` - List of database schema(s). For example: `public`.
* `schema-name` - The name (slug) of the schema repository in Atlas Registry.
  Read more in Atlas website: https://atlasgo.io/registry.
* `tag` - The tag to apply to the pushed schema. By default, the current git commit hash is used.
* `url` - Desired schema URL(s) to push. For example: `file://schema.lt.hcl`.
* `version` - The version of the schema.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).



#### Outputs

* `link` - Link to the schema version on Atlas.
* `slug` - The slug of the pushed schema version.
* `url` - The URL of the pushed schema version.


### `ariga/atlas-action/schema/plan`

Plan a declarative migration for a schema transition.

#### Inputs

* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `from` - URL(s) of the current schema state. If not provided, Atlas uses the [`url`](https://atlasgo.io/hcl/config#env.url) from the config file.
  If that is also not set, Atlas defaults to the **last known state in the Atlas Registry** (use [`schema.repo`](https://atlasgo.io/hcl/config#env.schema.repo) from the config file).
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `name` - The name of the plan. By default, Atlas will generate a name based on the schema changes.
* `schema` - List of database schema(s). For example: `public`.
* `schema-name` - The name (slug) of the schema repository in Atlas Registry.
  Read more in Atlas website: https://atlasgo.io/registry.
* `to` - URL(s) of the desired schema state.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).



#### Outputs

* `link` - Link to the schema plan on Atlas.
* `plan` - The plan to be applied or generated. (e.g. `atlas://<schema>/plans/<id>`)
* `status` - The status of the plan. For example, `PENDING` or `APPROVED`.


### `ariga/atlas-action/schema/plan/approve`

Approve a declarative migration plan.

#### Inputs

* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `from` - URL(s) of the current schema state. If not provided, Atlas uses the [`url`](https://atlasgo.io/hcl/config#env.url) from the config file.
  If that is also not set, Atlas defaults to the **last known state in the Atlas Registry** (use [`schema.repo`](https://atlasgo.io/hcl/config#env.schema.repo) from the config file).
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `plan` - The URL of the plan to be approved. For example, `atlas://<schema>/plans/<id>`.
  If not provided, Atlas will search the registry for a plan corresponding to the given schema transition and approve it
  (typically, this plan is created during the PR stage). If multiple plans are found, an error will be thrown.
* `schema` - List of database schema(s). For example: `public`.
* `schema-name` - The name (slug) of the schema repository in Atlas Registry.
  Read more in Atlas website: https://atlasgo.io/registry.
* `to` - URL(s) of the desired schema state.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to use for analysis. For example: `mysql://root:pass@localhost:3306/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).



#### Outputs

* `link` - Link to the schema plan on Atlas.
* `plan` - The plan to be applied or generated. (e.g. `atlas://<schema>/plans/<id>`)
* `status` - The status of the plan. (e.g, `PENDING`, `APPROVED`)


### `ariga/atlas-action/monitor/schema`

Monitor changes of the database schema and track them in Atlas Cloud.
Can be used periodically to [monitor](https://atlasgo.io/monitoring) changes in the database schema.

#### Inputs

* `cloud-token` - (Required) The token that is used to connect to Atlas Cloud (should be passed as a secret).
* `collect-stats` - Whether to collect and send anonymized usage statistics to Atlas.
* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `schemas` - List of database schemas to include (by default includes all schemas). see: https://atlasgo.io/declarative/inspect#inspect-multiple-schemas
* `slug` - Optional unique identifier for the database server.
* `url` - URL of the database to sync (mutually exclusive with `config` and `env`).
* `config` - The URL of the Atlas configuration file (mutually exclusive with `url`).
  For example, `file://config/atlas.hcl`, learn more about
  [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file.
  For example, `dev` (mutually exclusive with `url`).



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