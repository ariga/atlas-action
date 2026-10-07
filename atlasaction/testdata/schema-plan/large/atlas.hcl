// Creates 120 tables. The change summary is collapsed and, to fit GitHub's
// size limit, shows statement counts instead of statements.
env "test" {
  dev = "sqlite://dev?mode=memory"
  schema {
    src = "file://to.sql"
    repo {
      name = "atlas-action"
    }
  }
}
