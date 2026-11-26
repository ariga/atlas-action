# GitHub Actions for Atlas

This repository contains GitHub Actions for working with [Atlas](https://atlasgo.io).

To learn more about the recommended way to build workflows, read our guide on 
[Modern CI/CD for Databases](https://atlasgo.io/guides/modern-database-ci-cd).

## Actions

| Action                                                                        | Description                                                                         |
|-------------------------------------------------------------------------------|-------------------------------------------------------------------------------------|
| [ariga/setup-atlas](#arigasetup-atlas)                                        | Setup the Atlas CLI and optionally login to Atlas Cloud                             |
| [ariga/atlas-action/copilot](#arigaatlas-actioncopilot)                       | Talk to Atlas Copilot.                                                              |
| [ariga/atlas-action/migrate/apply](#arigaatlas-actionmigrateapply)            | Applies a migration directory on a target database                                  |
| [ariga/atlas-action/migrate/autorebase](#arigaatlas-actionmigrateautorebase)  | Automatically resolves `atlas.sum` conflicts and rebases the migration directory onto the target branch. |
| [ariga/atlas-action/migrate/hash](#arigaatlas-actionmigratehash)              | Automatically generate a hash of the schema migrations directory, and commit it to the migration directory. |
| [ariga/atlas-action/migrate/diff](#arigaatlas-actionmigratediff)              | Automatically generate versioned migrations whenever the schema is changed, and commit them to the migration directory. |
| [ariga/atlas-action/migrate/down](#arigaatlas-actionmigratedown)              | Reverts deployed migration files on a target database                               |
| [ariga/atlas-action/migrate/drift](#arigaatlas-actionmigratedrift)            | Detect schema drift between a target database and its migration history             |
| [ariga/atlas-action/migrate/lint](#arigaatlas-actionmigratelint)              | CI for database schema changes with Atlas                                           |
| [ariga/atlas-action/migrate/push](#arigaatlas-actionmigratepush)              | Push the current version of your migration directory to Atlas Cloud.                |
| [ariga/atlas-action/migrate/set](#arigaatlas-actionmigrateset)                | Edits the revision table to consider all migrations up to and including the given version to be applied. |
| [ariga/atlas-action/migrate/test](#arigaatlas-actionmigratetest)              | CI for database schema changes with Atlas                                           |
| [ariga/atlas-action/monitor/schema](#arigaatlas-actionmonitorschema)          | Sync the database schema to Atlas Cloud.                                            |
| [ariga/atlas-action/schema/apply](#arigaatlas-actionschemaapply)              | Applies schema changes to a target database                                         |
| [ariga/atlas-action/schema/lint](#arigaatlas-actionschemalint)                | Lint database schema with Atlas                                                     |
| [ariga/atlas-action/schema/plan](#arigaatlas-actionschemaplan)                | Plan a declarative migration to move from the current state to the desired state    |
| [ariga/atlas-action/schema/plan/approve](#arigaatlas-actionschemaplanapprove) | Approve a migration plan by its URL                                                 |
| [ariga/atlas-action/schema/push](#arigaatlas-actionschemapush)                | Push a schema version with an optional tag to Atlas                                 |
| [ariga/atlas-action/schema/test](#arigaatlas-actionschematest)                | Run schema tests against the desired schema                                         |
| [ariga/atlas-action/security/scan](#arigaatlas-actionsecurityscan)            | Scan databases for security issues with Atlas                                       |

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

On pull requests, the action comments with the lint results. On GitHub, if the Atlas CLI reports a change summary,
the comment also lists the changed objects of each file with their diffs and statements.

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
* `wait-interval` - Duration between approval checks, e.g. `30s` or `5m`.
  Each check re-inspects the target database, so prefer `30s` or more when `wait-timeout` is long.
* `wait-timeout` - How long to wait for approval, e.g. `30m`.
  If unset, the action does not wait: it creates the plan, prints its link and fails. Re-run the action after approving.
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

### `ariga/atlas-action/migrate/drift`

Detect schema drift between a target database and its migration history. The action compares the state of
the database against the state its migration directory defines at the last applied version, and reports the
objects that diverged from it. Pending migration files are not considered drift. Read more about
[drift detection](https://atlasgo.io/versioned/drift-detection#migrate-drift).

The expected state is fetched from the Atlas Registry when the migration directory is stored there, e.g.
`atlas://app`. Otherwise, it is computed by replaying the directory on the dev database given by `dev-url`.
The action requires a login to Atlas Cloud. It fails when drift is detected, or when a database could not
be checked.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `dir` - The URL of the migration directory that defines the expected state. For example: `atlas://dir-name`
  for cloud based directories or `file://migrations` for local ones.
* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `revisions-schema` - The name of the schema containing the revisions table.
* `url` - The URL of the target database to check for drift. For example: `mysql://root:pass@localhost:3306/prod`.
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.
* `dev-url` - The URL of the dev-database to compute the expected state on. Required when the migration
  directory is not stored in the Atlas Registry. For example: `docker://postgres/17/dev`.
  Read more about [dev-databases](https://atlasgo.io/concepts/dev-database).

#### Outputs

* `drifted` - Whether drift was detected in any of the checked databases. Either "true" or "false".
* `report` - A JSON array of the drift reports, one for each checked database.

#### Usage

The following workflow checks the production database for drift every hour:

```yaml
name: Drift Detection
on:
  schedule:
    - cron: '0 * * * *'
jobs:
  drift:
    runs-on: ubuntu-latest
    steps:
      - uses: ariga/setup-atlas@v0
        with:
          cloud-token: ${{ secrets.ATLAS_TOKEN }}
      - uses: ariga/atlas-action/migrate/drift@v1
        with:
          url: ${{ secrets.DATABASE_URL }}
          dir: atlas://app
```

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
> After the rebase is done and a commit is pushed by the action, no `push` workflows are triggered, and the `pull_request` workflows wait for approval, unless the action is running with a personal access token (PAT).
>```
>   - uses: actions/checkout@v4
>     with:
>       token: ${{ secrets.PAT }}
>```


#### Inputs

All inputs are optional

* `base-branch` - The base branch to rebase the migration directory onto. Default to the default branch of the repository.
* `base-sha` - Optional. When set, use this git commit SHA as the base instead of base-branch. Requires the git server to support fetch-by-SHA (e.g. GitHub, GitLab).
* `dir` - The URL of the migration directory to rebase on. By default: `file://migrations`.
* `remote` - The remote to fetch from. Defaults to `origin`.
* `force-rebase` - When true, skip the merge step and rebase whenever there are dev-only migrations (e.g. out-of-order workflow after merging base and running apply with exec-order non-linear). Default is false.
* `stack` - When true, restack GitHub stacked pull requests whose migrations conflict: rebase each pull request onto the one below it, from the bottom of the stack up, place only the migration files each pull request adds on top of the ones below it, and push all the branches of the stack at once. A pull request that is not stacked, and targets the base branch, is rebased as without this input. GitHub Actions only, and cannot be used with `base-sha` or `force-rebase`. Default is false. See [Stacked pull requests](#stacked-pull-requests).
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

#### Stacked pull requests

In a [stack of pull requests](https://docs.github.com/en/pull-requests/get-started/about-stacked-prs),
each pull request targets the branch of the one below it, and every migration that lands on the stack's
base branch (its trunk), or on a lower branch, makes the `atlas.sum` files of the branches above it conflict.
A stack with conflicts needs a manual restack before it can merge. When the bottom pull request merges,
GitHub rebases the pull requests above it, but a rebase cannot resolve conflicting `atlas.sum` files. With
`stack: true`, the action restacks such stacks:

* It runs on a push to a branch of a stack, and restacks that stack. It also restacks each open stack that
  targets the pushed branch, such as `main` or a branch of another stack, and each stack that targets a
  branch it restacked.
* Like `gh stack rebase`, it rebases each open pull request onto the one below it, from the bottom of the
  stack up, and replays only the commits of the pull request. It tells them from the commits of the pull
  request below it by the heads that branch had, which it reads from the repository activity on GitHub.
  It moves only the migration files each pull request adds on top of the ones below it, with
  `atlas migrate rebase`, and adds one commit per pull request, which later runs amend while it is the last
  commit of the branch.
* Like `gh stack push`, it pushes all the restacked branches at once, with `--force-with-lease`. If a pull
  request does not target the branch of the pull request below it (or the base of the stack, for the bottom one),
  conflicts in other files, has a merge commit with changes of its own or with more than two parents (which a
  rebase drops), edits migration files of the pull request below it once that pull request's migrations change,
  has commits the pull request below it no longer has (they may have been dropped there, or moved up, like when a
  branch is split into a stack), or its commits cannot be told apart, nothing is pushed, and the run fails with
  the reason. Resolve it locally with `gh stack rebase`.
* It does nothing when the migrations of the stack do not conflict, even if the stack is not linear, and
  leaves the rebase to GitHub, which can rebase the stack from the pull request.
* A pull request that is not stacked, and targets the base branch, is rebased as without the input.
  Branches without pull requests, and pull requests that target other branches, are left as they are.
* It runs only on GitHub Actions.

```yaml
name: Restack Atlas Migrations
on:
  # Run on every push to a branch, as pull request workflows do not run while a pull
  # request has conflicts, and stacks need a restack when the branch they target moves.
  push:
    branches: ['**']
jobs:
  migrate-auto-rebase:
    # Restacks must not overlap. Push events carry no stack number to key on, so all the runs
    # share one group, and up to 100 of them wait in line; GitHub cancels the ones beyond that.
    # As the runs are handled one at a time, restacks lag behind when pushes come faster than a
    # run finishes.
    concurrency:
      group: atlas-autorebase
      queue: max
    permissions:
      # Allow pushing the restacked branches.
      contents: write
      # Allow reading the pull requests and their stacks.
      pull-requests: read
    runs-on: ubuntu-latest
    steps:
    - uses: ariga/setup-atlas@v0
      with:
        cloud-token: ${{ secrets.ATLAS_TOKEN }}
    - uses: actions/checkout@v4
      with:
        # Use personal access token to trigger workflows after the branches are pushed by the action.
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
        dir: file://migrations
        stack: true
      env:
        GITHUB_TOKEN: ${{ github.token }}
```

> **Note**
>
> * The action rewrites the branches of the stack. Update your local branches before you continue
>   working on them, for example with `git pull --rebase`.
> * The action pushes with the token of `actions/checkout`. With the default `GITHUB_TOKEN`, the pushes
>   start no `push` workflows, and the `pull_request` workflows they start wait for approval. With a personal
>   access token, each branch the action pushes starts one more run of the workflow, which finds the stack
>   restacked and does nothing.
> * A token without the `workflow` scope, like `GITHUB_TOKEN`, cannot push a restack that creates a new
>   version of a workflow file. This happens when a pull request changes a workflow file that also
>   changed below it.
> * Unlike `gh stack push`, which skips queued branches, the action also restacks the pull requests that are
>   in the merge queue.
> * A branch that requires signed commits rejects the restack, as the action recreates the commits of the
>   pull request without signatures, unless the workflow sets up commit signing.
> * The action reads the heads each branch had from the activity of the repository, which requires the
>   `contents: read` permission, included in `contents: write`. This covers the branches
>   of the stack, and a base branch that has a pull request of its own, like a `develop` branch with a pull
>   request to `main`, as such branches may be rewritten. An activity that does not go back to the creation
>   of the branch is enough when it records no force-push of the branch, and goes back to a head the pull
>   request above it has. When it is not enough, or cannot be read, the pull request above the branch is
>   restacked only if it is built on the latest head of the branch, or on the head it had before the push that
>   triggered the run, and keeps the commits the branch dropped before, if any. Otherwise, the action leaves
>   the pull requests as they are. The heads a branch had before it was force-pushed are not in the clone, so
>   the action fetches them by their SHA, and leaves the pull requests above it as they are if one cannot be
>   fetched. Restack them locally with `gh stack rebase`.
> * A base branch without a pull request of its own, like `main`, is expected to only move forward. The action
>   reads only its force-pushes, to find the commits it dropped, and skips the heads it cannot fetch, or all of
>   them if the activity endpoint is not available. If reading them fails otherwise, the bottom pull request is
>   restacked only if it is built on the head the branch had before the push that triggered the run.
> * When the base branch of a stack is deleted, like after its pull request merged, GitHub moves the stack onto a
>   new base without rebasing it. A restack then replays the commits of the deleted branch that the new base does
>   not have into the bottom pull request, like `git rebase` does, while `gh stack rebase` drops them.
> * A restack resolves only the `atlas.sum` file of the `dir` input. If the stack and the branch it targets
>   both change another migration directory, restack it locally.
> * In other formats than Atlas's, like `golang-migrate` or `flyway`, the action replays the commits of the pull
>   requests, but cannot move their migration files on top, as `atlas migrate rebase` renames only files of the
>   Atlas format. A pull request whose migrations do not sort after the ones below it, or have their versions, is
>   left as it is, and the run fails. Files without a version, like the repeatable migrations of Flyway, are not
>   ordered: the restack replays their changes, and regenerates `atlas.sum`.
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
* `base-sha` - Optional. When set, use this git commit SHA as the base instead of base-branch. Requires the git server to support fetch-by-SHA (e.g. GitHub, GitLab).
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
  With "WARNING" and "ERROR", set the same policy in the `lint { review }` block of the project config. Otherwise,
  changes that do not require approval are applied with the config policy (default "ALWAYS"), and the step fails.
  If unset, and the project config requires approval for the changes, the step fails. In this case, set this
  input (plus `wait-timeout`/`wait-interval`) to create and approve plans during deployment, or re-run the PR's
  `schema/plan` step.
* `plan` - The plan to apply. For example, `atlas://<schema>/plans/<id>`.
* `schema` - List of database schema(s). For example: `public`.
* `to` - URL(s) of the desired schema state.
* `tx-mode` - Transaction mode to use. Either "file", "all", or "none".
* `url` - The URL of the target database to apply changes to.
  For example: `mysql://root:pass@localhost:3306/prod`.
* `wait-interval` - Duration between approval checks, e.g. `30s` or `5m`.
  Each check re-inspects the target database, so prefer `30s` or more when `wait-timeout` is long.
* `wait-timeout` - How long to wait for approval, e.g. `30m`.
  If unset, the action does not wait: it creates the plan, prints its link and fails. Re-run the action after approving.
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
* `plan` - With `lint-review`, the plan that the action applies or waits for, e.g. `atlas://<schema>/plans/<name>`.
* `plan-status` - With `lint-review`, the last known status of the `plan` output. For example, `PENDING` or `APPROVED`.

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

On pull requests, the action comments with the plan and its lint results. On GitHub, if the Atlas CLI reports a change
summary, the comment also lists each changed object with its diff and statements. Set the `change-summary` input to
`collapsed` or `hidden` to have reviewers see the migration script first, and `migration-script` to choose whether the
script starts open.

#### Inputs

* `change-summary` - How the plan comment shows the change summary on GitHub. By default, the summary is open (collapsed past
  15 objects) and the migration script below it is collapsed. Set `collapsed` to collapse the summary and open
  the migration script, or `hidden` to leave the summary out and show the migration script in its place.
* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `from` - URL(s) of the current schema state. If not provided, Atlas uses the [`url`](https://atlasgo.io/hcl/config#env.url) from the config file.
  If that is also not set, Atlas defaults to the **last known state in the Atlas Registry** (use [`schema.repo`](https://atlasgo.io/hcl/config#env.schema.repo) from the config file).
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `migration-script` - How the plan comment shows the migration script on GitHub. By default, it is collapsed below the change
  summary, and open when `change-summary` is set or the comment has no summary. Set `expanded` to always
  show it open, or `collapsed` to always collapse it.
* `name` - The explicit name of the plan.
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

* `link` - Link to the schema plan on Atlas Cloud. Empty if the plan is not in Atlas Cloud.
* `plan` - The plan to be applied or generated. (e.g. `atlas://<schema>/plans/<id>`)
* `status` - The status of the plan. For example, `PENDING` or `APPROVED`.


### `ariga/atlas-action/schema/plan/approve`

Approve a declarative migration plan.

Without the `plan` input, the action approves the pending plan of the schema transition, typically the plan that
`schema/plan` created in the pull request. It skips the plans named `commit-<commit>-<random>`, which `schema/apply`
creates for a deployment outside a pull request, so that re-running such a deployment while it waits for its plan
does not approve the plan.

#### Inputs

* `exclude` - List of glob patterns used to select which resources to filter in inspection
  see: https://atlasgo.io/declarative/inspect#exclude-schemas
* `from` - URL(s) of the current schema state. If not provided, Atlas uses the [`url`](https://atlasgo.io/hcl/config#env.url) from the config file.
  If that is also not set, Atlas defaults to the **last known state in the Atlas Registry** (use [`schema.repo`](https://atlasgo.io/hcl/config#env.schema.repo) from the config file).
* `include` - List of glob patterns used to select which resources to keep in inspection
  see: https://atlasgo.io/declarative/inspect#include-schemas
* `plan` - The URL of the plan to approve, for example `atlas://<schema>/plans/<name>`.
  If not set, the action searches the registry for the pending plan of the schema transition and approves it,
  typically the plan that `schema/plan` created in the pull request. It skips the plans named
  `commit-<commit>-<random>`, which `schema/apply` creates for a deployment outside a pull request.
  If several plans are found, the action fails.
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

* `link` - Link to the schema plan on Atlas Cloud. Empty if the plan is not in Atlas Cloud.
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

### `ariga/atlas-action/security/scan`

Scan databases for security issues with Atlas. The scan reports the known vulnerabilities of the
database server version and of the extensions installed in the database, as recorded in the Atlas
Security Graph. It requires a login to Atlas Cloud, on a plan that includes the Security Graph.

The action fails when a database could not be scanned, as that leaves its state unknown. The issues
it reports do not fail it; set `fail-on` to the severity an issue must reach to fail the action.

#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.

* `fail-on` - The lowest severity that fails the action. By default, reported issues do not
  fail it, and only a database that could not be scanned does.
* `ignore` - CVE identifier(s) not to report. For example: `CVE-2017-18359`.
* `min-severity` - The lowest severity to report. For example, `HIGH` reports only the `HIGH`
  and `CRITICAL` issues.
* `urls` - URL(s) of the target databases, one per line.
  For example: `postgres://localhost:5432/app?sslmode=disable`.
  If not set, Atlas uses the [`url`](https://atlasgo.io/hcl/config#env.url) from the config file.
  Read more about [Atlas URLs](https://atlasgo.io/concepts/url).
* `working-directory` - Atlas working directory. Default is project root
* `config` - The URL of the Atlas configuration file. By default, Atlas will look for a file
  named `atlas.hcl` in the current directory. For example, `file://config/atlas.hcl`.
  Learn more about [Atlas configuration files](https://atlasgo.io/atlas-schema/projects).
* `env` - The environment to use from the Atlas configuration file. For example, `dev`.
* `vars` - A JSON object containing variables to be used in the Atlas configuration file.
  For example, `{"var1": "value1", "var2": "value2"}`.

#### Outputs

* `count` - The number of security issues that were reported.
* `failures` - The number of databases that could not be scanned.
* `report` - A JSON report of the scan, containing the issues found in each scanned database.

#### Usage

The following action scans two databases, reports every issue it finds, and fails the workflow if
any of them is graded `HIGH` or above.

```yaml
        uses: ariga/atlas-action/security/scan@v1
        with:
          urls: |-
            postgres://postgres:pass@localhost:5432/app?sslmode=disable
            postgres://postgres:pass@localhost:5432/reports?sslmode=disable
          fail-on: HIGH
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
      - uses: ariga/setup-atlas@v0
        with:
          cloud-token: ${{ secrets.ATLAS_TOKEN }}
      # This action builds the binary locally, 
      # this action won't replace `ariga/setup-atlas` action. You need both.
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