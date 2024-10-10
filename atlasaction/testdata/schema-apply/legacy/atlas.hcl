env "test" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  schema {
    src = [for f in glob("*.lt.hcl"): format("file://%s", f)]
    repo {
      name = "atlas-action"
    }
  }
}
