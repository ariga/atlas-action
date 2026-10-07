// Creates and drops tables, adds a column, changes one line of a long table
// and rewrites the columns of another. SQLite rebuilds the changed tables,
// with statements that change no object. It has no views, since Atlas can't
// modify views on SQLite.
env "test" {
  dev = "sqlite://dev?mode=memory"
  schema {
    src = "file://to.sql"
    repo {
      name = "atlas-action"
    }
  }
  lint {
    // Report the dropped table as a warning, so the plan is created.
    destructive {
      error = false
    }
  }
}
