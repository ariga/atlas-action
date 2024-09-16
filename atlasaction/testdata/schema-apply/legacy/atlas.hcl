env "test" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  src = glob("*.lt.hcl")
  schema {
    src = "atlas://atlas-action"
    repo {
      name = "atlas-action"
    }
  }
}
