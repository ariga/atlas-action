env "test" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  schema {
    src = "file://schema.lt.hcl"
    repo {
      name = "atlas-action"
    }
  }
}

env "local" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  src = "file://schema.lt.hcl"
}
