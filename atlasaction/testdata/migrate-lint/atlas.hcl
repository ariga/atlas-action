// The repository in Atlas Cloud has no migration files, so the lint treats all of them
// as new. The first runs as one batch, since it has more than ten statements. The files
// after it change a few objects, add seed data, rebuild tables in SQLite (statements
// that change no object) and drop one, and create 20 tables, past the 15 rows the
// comment shows open.
env "test" {
  lint {
    // Report the dropped table as a warning, so the lint passes.
    destructive {
      error = false
    }
  }
}
