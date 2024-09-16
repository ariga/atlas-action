env "test" {
  url = "sqlite://local.db"
  dev = "sqlite://file?mode=memory"
  src = "file://schema.lt.hcl"
}
